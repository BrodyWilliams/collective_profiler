//
// Copyright (c) 2020, NVIDIA CORPORATION. All rights reserved.
//
// See LICENSE.txt for license information
//

package patterns

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/counts"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/notation"
)

const (
	// SummaryFilePrefix is the prefix of all the pattern summary files
	SummaryFilePrefix = "patterns-summary-"
)

type CallData struct {
	Send  map[int]int
	Recv  map[int]int
	Count int
	Calls []int
}

// GlobalPatterns holds the data all the patterns the infrastructure was able to detect
type Data struct {
	// AllPatterns is the data for all the patterns that have been detected
	AllPatterns []*CallData

	// OneToN is the data of all the patterns that fits with a 1 -> N scheme
	OneToN []*CallData

	// NToN is the data of all the patterns where N ranks exchange data between all of them
	NToN []*CallData

	// NoToOne is the data of all the patterns that fits with a N -> 1 scheme
	NToOne []*CallData

	// Empty is the data of all the patterns that do not exchange any data (all counts are equal to 0)
	Empty []*CallData
}

func CompareCallPatterns(p1 map[int]int, p2 map[int]int) bool {
	if len(p1) != len(p2) {
		return false
	}

	return reflect.DeepEqual(p1, p2)
}

func GetPatternHeader(reader *bufio.Reader) ([]int, string, error) {
	var callIDs []int
	callIDsStr := ""

	line, readerErr := reader.ReadString('\n')
	if readerErr != nil {
		return callIDs, callIDsStr, readerErr
	}

	// Are we at the beginning of a metadata block?
	if !strings.HasPrefix(line, "## Pattern #") {
		return callIDs, callIDsStr, fmt.Errorf("[ERROR] not a header (line: %s)", line)
	}

	line, readerErr = reader.ReadString('\n')
	if readerErr != nil {
		return callIDs, callIDsStr, readerErr
	}

	if !strings.HasPrefix(line, "Alltoallv calls: ") {
		return callIDs, callIDsStr, fmt.Errorf("[ERROR] not a header (line: %s)", line)
	}

	var err error
	callIDsStr = strings.TrimLeft(line, "Alltoallv calls: ")
	callIDsStr = strings.TrimRight(callIDsStr, "\n")
	callIDs, err = notation.ConvertCompressedCallListToIntSlice(callIDsStr)
	if err != nil {
		return callIDs, callIDsStr, err
	}

	return callIDs, callIDsStr, nil
}

// Same compare two patterns
func Same(patterns1, patterns2 Data) bool {
	return sameListOfPatterns(patterns1.AllPatterns, patterns2.AllPatterns)
}

func displayPatterns(pattern []*CallData) {
	for _, p := range pattern {
		for numPeers, numRanks := range p.Send {
			fmt.Printf("%d ranks are sending to %d other ranks\n", numRanks, numPeers)
		}
		for numPeers, numRanks := range p.Recv {
			fmt.Printf("%d ranks are receiving from %d other ranks\n", numRanks, numPeers)
		}
	}
}

// patternIsInList checks whether a given pattern is in a list of patterns. If so, it returns the
// number of alltoallv calls that have the pattern, otherwise it returns 0
func patternIsInList(numPeers int, numRanks int, ctx string, patterns []*CallData) int {
	for _, p := range patterns {
		if ctx == "SEND" {
			for numP, numR := range p.Send {
				if numP == numP && numRanks == numR {
					return p.Count
				}
			}
		} else {
			for numP, numR := range p.Recv {
				if numP == numP && numRanks == numR {
					return p.Count
				}
			}
		}
	}
	return 0
}

func sameListOfPatterns(patterns1, patterns2 []*CallData) bool {
	// reflect.DeepEqual cannot be used here

	// Compare send counts
	for _, p1 := range patterns1 {
		for numPeers, numRanks := range p1.Send {
			count := patternIsInList(numPeers, numRanks, "SEND", patterns2)
			if count == 0 {
				return false
			}
			if p1.Count != count {
				log.Printf("Send counts differ: %d vs. %d", p1.Count, count)
			}
		}
	}

	// Compare recv counts
	for _, p1 := range patterns1 {
		for numPeers, numRanks := range p1.Recv {
			count := patternIsInList(numPeers, numRanks, "RECV", patterns2)
			if count == 0 {
				return false
			}
			if p1.Count != count {
				log.Printf("Recv counts differ: %d vs. %d", p1.Count, count)
			}
		}
	}

	return true
}

func NoSummary(d Data) bool {
	if len(d.OneToN) != 0 {
		return false
	}

	if len(d.NToOne) != 0 {
		return false
	}

	if len(d.NToN) != 0 {
		return false
	}

	return true
}

// GetFilePath returns the full path to the pattern file associated to a rank within a job
func GetFilePath(basedir string, jobid int, rank int) string {
	return filepath.Join(basedir, fmt.Sprintf("patterns-job%d-rank%d.md", jobid, rank))
}

// GetSummaryFilePath returns the full path to the pattern summary file associated to a rank within a job
func GetSummaryFilePath(basedir string, jobid int, rank int) string {
	return filepath.Join(basedir, fmt.Sprintf("%sjob%d-rank%d.md", SummaryFilePrefix, jobid, rank))
}

func getPatterns(reader *bufio.Reader) (string, error) {
	patterns := ""

	for {
		line, readerErr := reader.ReadString('\n')
		if readerErr != nil && readerErr != io.EOF {
			return patterns, readerErr
		}
		if readerErr == io.EOF {
			break
		}

		if line == "" || line == "\n" {
			// end of pattern data
			break
		}

		if strings.HasPrefix(line, "Alltoallv calls: ") {
			return patterns, fmt.Errorf("header and patterns parser are compromised: %s", line)
		}

		patterns += line

	}

	return patterns, nil
}

// GetCall extracts the patterns associated to a specific alltoallv call
func GetCall(dir string, jobid int, rank int, callNum int) (string, error) {
	patternsOutputFile := GetFilePath(dir, jobid, rank)
	patternsFd, err := os.Open(patternsOutputFile)
	if err != nil {
		return "", err
	}
	defer patternsFd.Close()
	patternsReader := bufio.NewReader(patternsFd)

	// The very first line should be '#Patterns'
	line, readerErr := patternsReader.ReadString('\n')
	if readerErr != nil {
		return "", readerErr
	}
	if line != "# Patterns\n" {
		return "", fmt.Errorf("wrong file format: %s", line)
	}

	for {
		callIDs, _, err := GetPatternHeader(patternsReader)
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("unable to read %s: %w", patternsOutputFile, err)
		}
		if err == io.EOF {
			break
		}

		targetBlock := false
		for _, c := range callIDs {
			if c == callNum {
				targetBlock = true
				break
			}
		}

		if targetBlock {
			patterns, err := getPatterns(patternsReader)
			if err != nil {
				return "", nil
			}
			return patterns, nil
		} else {
			_, err := getPatterns(patternsReader)
			if err != nil {
				return "", nil
			}
		}
	}

	return "", fmt.Errorf("unable to find data for call %d", callNum)
}

func (d *Data) addPattern(callNum int, sendPatterns map[int]int, recvPatterns map[int]int) error {
	for idx, x := range d.AllPatterns {
		if CompareCallPatterns(x.Send, sendPatterns) && CompareCallPatterns(x.Recv, recvPatterns) {
			// Increment count for pattern
			log.Printf("-> Alltoallv call #%d - Adding alltoallv to pattern %d...\n", callNum, idx)
			x.Count++
			x.Calls = append(x.Calls, callNum)

			return nil
		}
	}

	// If we get here, it means that we did not find a similar pattern
	log.Printf("-> Alltoallv call %d - Adding new pattern...\n", callNum)
	new_cp := new(CallData)
	new_cp.Send = sendPatterns
	new_cp.Recv = recvPatterns
	new_cp.Count = 1
	new_cp.Calls = append(new_cp.Calls, callNum)
	d.AllPatterns = append(d.AllPatterns, new_cp)

	// Detect specific patterns using the send counts only, e.g., 1->n, n->1 and n->n
	// Note: we do not need to check the receive side because if n ranks are sending to n other ranks,
	// we know that n ranks are receiving from n other ranks with equivalent counts. Send/receive symmetry.
	for sendTo, n := range sendPatterns {
		// Detect 1->n patterns
		if sendTo > n*100 {
			d.OneToN = append(d.OneToN, new_cp)
			continue
		}

		// Detect n->n patterns
		if sendTo == n {
			d.NToN = append(d.NToN, new_cp)
			continue
		}

		// Detect n->1 patterns
		if sendTo*100 < n {
			d.NToOne = append(d.NToOne, new_cp)
			continue
		}
	}

	return nil
}

func writeDataToFile(fd *os.File, cd *CallData) error {
	for sendTo, n := range cd.Send {
		_, err := fd.WriteString(fmt.Sprintf("%d ranks sent to %d other ranks\n", n, sendTo))
		if err != nil {
			return err
		}
	}
	for recvFrom, n := range cd.Recv {
		_, err := fd.WriteString(fmt.Sprintf("%d ranks recv'd from %d other ranks\n", n, recvFrom))
		if err != nil {
			return err
		}
	}
	return nil
}

func WriteToFile(fd *os.File, num int, totalNumCalls int, cd *CallData) error {
	_, err := fd.WriteString(fmt.Sprintf("## Pattern #%d (%d/%d alltoallv calls)\n\n", num, cd.Count, totalNumCalls))
	if err != nil {
		return err
	}
	_, err = fd.WriteString(fmt.Sprintf("Alltoallv calls: %s\n", notation.CompressIntArray(cd.Calls)))
	if err != nil {
		return err
	}

	err = writeDataToFile(fd, cd)
	if err != nil {
		return err
	}

	_, err = fd.WriteString("\n")
	if err != nil {
		return err
	}

	return nil
}

func WriteSubcommNtoNPatterns(fd *os.File, ranks []int, stats map[int]counts.SendRecvStats, patterns map[int]Data) error {
	_, err := fd.WriteString("## N to n patterns\n\n")
	if err != nil {
		return err
	}

	// Print the pattern, which is the same for all ranks if we reach this function
	_, err = fd.WriteString("\n### Pattern(s) description\n\n")
	if err != nil {
		return err
	}
	for _, p := range patterns[ranks[0]].NToN {
		err := writeDataToFile(fd, p)
		if err != nil {
			return err
		}
	}

	_, err = fd.WriteString("\n\n### Sub-communicator(s) information\n\n")
	if err != nil {
		return err
	}
	for _, r := range ranks {
		// Print metadata for the subcomm
		_, err := fd.WriteString(fmt.Sprintf("-> Subcommunicator led by rank %d:\n", r))
		if err != nil {
			return err
		}
		num := 0
		for _, p := range patterns[r].NToN {
			_, err := fd.WriteString(fmt.Sprintf("\tpattern #%d: %d/%d alltoallv calls\n", num, p.Count, stats[r].TotalNumCalls))
			if err != nil {
				return err
			}
			num++
		}
	}

	return nil
}

func WriteSubcomm1toNPatterns(fd *os.File, ranks []int, stats map[int]counts.SendRecvStats, patterns map[int]Data) error {
	_, err := fd.WriteString("## 1 to n patterns\n\n")
	if err != nil {
		return err
	}

	// Print the pattern, which is the same for all ranks if we reach this function
	_, err = fd.WriteString("\n### Pattern(s) description\n\n")
	if err != nil {
		return err
	}
	for _, p := range patterns[ranks[0]].OneToN {
		err := writeDataToFile(fd, p)
		if err != nil {
			return err
		}
	}

	_, err = fd.WriteString("\n\n### Sub-communicator(s) information\n\n")
	if err != nil {
		return err
	}
	for _, r := range ranks {
		// Print metadata for the subcomm
		_, err := fd.WriteString(fmt.Sprintf("-> Subcommunicator led by rank %d:\n", r))
		if err != nil {
			return err
		}
		num := 0
		for _, p := range patterns[r].OneToN {
			_, err := fd.WriteString(fmt.Sprintf("\tpattern #%d: %d/%d alltoallv calls\n", num, p.Count, stats[r].TotalNumCalls))
			if err != nil {
				return err
			}
			num++
		}
	}

	return nil
}

func WriteSubcommNto1Patterns(fd *os.File, ranks []int, stats map[int]counts.SendRecvStats, patterns map[int]Data) error {
	_, err := fd.WriteString("## N to 1 patterns\n\n")
	if err != nil {
		return err
	}

	// Print the pattern, which is the same for all ranks if we reach this function
	_, err = fd.WriteString("\n### Pattern(s) description\n\n")
	if err != nil {
		return err
	}
	for _, p := range patterns[ranks[0]].NToOne {
		err := writeDataToFile(fd, p)
		if err != nil {
			return err
		}
	}

	_, err = fd.WriteString("\n\n### Sub-communicator(s) information\n\n")
	if err != nil {
		return err
	}
	for _, r := range ranks {
		// Print metadata for the subcomm
		_, err := fd.WriteString(fmt.Sprintf("-> Subcommunicator led by rank %d:\n", r))
		if err != nil {
			return err
		}
		num := 0
		for _, p := range patterns[r].NToOne {
			_, err := fd.WriteString(fmt.Sprintf("\tpattern #%d: %d/%d alltoallv calls\n", num, p.Count, stats[r].TotalNumCalls))
			if err != nil {
				return err
			}
			num++
		}
	}

	return nil
}

func ParseFiles(sendCountsFile string, recvCountsFile string, numCalls int, sizeThreshold int) (counts.SendRecvStats, Data, error) {

	var patterns Data
	callsCountsData := counts.NewSendRecvStats(sizeThreshold)

	for i := 0; i < numCalls; i++ {
		callCountsData, err := counts.ParseFiles(sendCountsFile, recvCountsFile, numCalls, sizeThreshold)
		if err != nil {
			return callCountsData, patterns, err
		}

		//displayCallPatterns(callInfo)
		// Analyze the send/receive pattern from the call
		err = patterns.addPattern(i, callCountsData.SendPatterns, callCountsData.RecvPatterns)
		if err != nil {
			return callCountsData, patterns, err
		}

		// We need to track calls that act like a barrier (no data exchanged)
		if callCountsData.TotalSendNonZeroCounts == 0 && callCountsData.TotalRecvNonZeroCounts == 0 {
			emptyPattern := new(CallData)
			emptyPattern.Count = 1
			emptyPattern.Calls = []int{i}
			patterns.Empty = append(patterns.Empty, emptyPattern)
		}

		// todo: update callsCountsData with the data from callCountsData
		callsCountsData.TotalNumCalls++
	}

	return callsCountsData, patterns, nil
}