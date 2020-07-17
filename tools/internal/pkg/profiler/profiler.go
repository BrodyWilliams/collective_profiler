//
// Copyright (c) 2020, NVIDIA CORPORATION. All rights reserved.
//
// See LICENSE.txt for license information
//

package profiler

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/analyzer"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/backtraces"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/counts"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/format"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/patterns"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/timings"

	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/datafilereader"
)

// OutputFileInfo gathers all the data for the handling of output files while analysis counts
type OutputFileInfo struct {
	// defaultFd is the file descriptor for the creation of the default output file while analyzing counts
	defaultFd *os.File

	// patternsFd is the file descriptor for the creation of the output files to store patterns discovered during the analysis of the counts
	patternsFd *os.File

	// patternsSummaryFd is the file descriptor for the creation of the summary output file for the patterns discovered during the analysis of the counts
	patternsSummaryFd *os.File

	// defaultOutputFile is the path of the file associated to DefaultFd
	defaultOutputFile string

	// patternsOutputFile is the path of the file associated to PatternsFd
	patternsOutputFile string

	// patternsSummaryOutputFile is the path of the file associated to SummaryPatternsFd
	patternsSummaryOutputFile string

	// Cleanup is the function to call after being done with all the files
	Cleanup func()
}

// CallInfo gathers all the data extracted about a specific alltoallv call
type CallInfo struct {
	// ID is the call number (zero-indexed)
	ID int

	// CountsData is the data gathered after parsing the send and receive counts files
	CountsData counts.CallData

	// Stats gathers all the communication patterns associated to the alltoallv call
	Patterns patterns.Data

	// PatternStr is the string version of the communication patterns
	PatternStr string

	// Timings represent all the timings associated to the alltoallv call (e.g., late arrival and execution timings)
	Timings timings.CallTimings

	// Backtrace is the string version of the alltoallv call's backtrace
	Backtrace string

	// SendStats gives all the statistics and data gathered while parsing the count file of the alltoallv call
	SendStats counts.Stats

	RecvStats counts.Stats
}

func LookupCall(sendCountsFile string, recvCountsFile string, numCall int, msgSizeThreshold int) (CallInfo, error) {
	var info CallInfo
	var err error

	info.CountsData, err = counts.LookupCall(sendCountsFile, recvCountsFile, numCall)
	if err != nil {
		return info, err
	}
	//info.CommSize = info.CountsStats.CommSize

	// todo: get the patterns here. Call counts.AnalyzeCounts?

	return info, nil
}

func containsCall(callNum int, calls []int) bool {
	for i := 0; i < len(calls); i++ {
		if calls[i] == callNum {
			return true
		}
	}
	return false
}

func GetCallRankData(sendCountersFile string, recvCountersFile string, callNum int, rank int) (int, int, error) {
	sendCounters, sendDatatypeSize, _, err := counts.ReadCallRankCounters([]string{sendCountersFile}, rank, callNum)
	if err != nil {
		return 0, 0, err
	}
	recvCounters, recvDatatypeSize, _, err := counts.ReadCallRankCounters([]string{recvCountersFile}, rank, callNum)
	if err != nil {
		return 0, 0, err
	}

	sendCounters = strings.TrimRight(sendCounters, "\n")
	recvCounters = strings.TrimRight(recvCounters, "\n")

	// We parse the send counters to know how much data is being sent
	sendSum := 0
	tokens := strings.Split(sendCounters, " ")
	for _, t := range tokens {
		if t == "" {
			continue
		}
		n, err := strconv.Atoi(t)
		if err != nil {
			return 0, 0, err
		}
		sendSum += n
	}
	sendSum = sendSum * sendDatatypeSize

	// We parse the recv counters to know how much data is being received
	recvSum := 0
	tokens = strings.Split(recvCounters, " ")
	for _, t := range tokens {
		if t == "" {
			continue
		}
		n, err := strconv.Atoi(t)
		if err != nil {
			return 0, 0, err
		}
		recvSum += n
	}
	recvSum = recvSum * recvDatatypeSize

	return sendSum, recvSum, nil
}

// AnalyzeSubCommsResults go through the results and analyzes results specific
// to sub-communicators cases
func AnalyzeSubCommsResults(dir string, stats map[int]counts.SendRecvStats, allPatterns map[int]patterns.Data) error {
	numPatterns := -1
	numNtoNPatterns := -1
	num1toNPatterns := -1
	numNto1Patterns := -1
	var referencePatterns patterns.Data

	// At the moment, we do a very basic analysis: are the patterns the same on all sub-communicators?
	for _, p := range allPatterns {
		if numPatterns == -1 {
			numPatterns = len(p.AllPatterns)
			numNto1Patterns = len(p.NToOne)
			numNtoNPatterns = len(p.NToN)
			num1toNPatterns = len(p.OneToN)
			referencePatterns = p
			continue
		}

		if numPatterns != len(p.AllPatterns) ||
			numNto1Patterns != len(p.NToOne) ||
			numNtoNPatterns != len(p.NToN) ||
			num1toNPatterns != len(p.OneToN) {
			return nil
		}

		if !patterns.Same(referencePatterns, p) {
			/*
				fmt.Println("Patterns differ:")
				displayPatterns(referencePatterns.AllPatterns)
				fmt.Printf("\n")
				displayPatterns(rankStats.Patterns.AllPatterns)
			*/
			return nil
		}
	}

	// If we get there it means all ranks, i.e., sub-communicators have the same amount of patterns
	log.Println("All patterns on all sub-communicators are similar")
	multicommHighlightFile := filepath.Join(dir, format.MulticommHighlightFilePrefix+".md")
	fd, err := os.OpenFile(multicommHighlightFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer fd.Close()

	_, err = fd.WriteString("Alltoallv on sub-communicators detected.\n\n# Patterns summary\n\n")
	if err != nil {
		return err
	}

	var ranks []int
	for r := range stats {
		ranks = append(ranks, r)
	}
	sort.Ints(ranks)

	if len(allPatterns[ranks[0]].NToN) > 0 {
		err := patterns.WriteSubcommNtoNPatterns(fd, ranks, stats, allPatterns)
		if err != nil {
			return err
		}
	}

	if len(allPatterns[ranks[0]].OneToN) > 0 {
		err := patterns.WriteSubcomm1toNPatterns(fd, ranks, stats, allPatterns)
		if err != nil {
			return err
		}
	}

	if len(allPatterns[ranks[0]].NToOne) > 0 {
		err := patterns.WriteSubcommNto1Patterns(fd, ranks, stats, allPatterns)
		if err != nil {
			return err
		}
	}

	_, err = fd.WriteString("\n## All 0 counts pattern; no data exchanged\n\n")
	if err != nil {
		return err
	}
	for _, rank := range ranks {
		if len(allPatterns[rank].Empty) > 0 {
			_, err = fd.WriteString(fmt.Sprintf("-> Sub-communicator led by rank %d: %d/%d alltoallv calls\n", rank, len(allPatterns[rank].Empty), stats[rank].TotalNumCalls))
			if err != nil {
				return err
			}
		}
	}

	// For now we save the bins' data separately because we do not have a good way at the moment
	// to mix bins and patterns (bins are specific to a count file, not a call; we could change that
	// but it would take time).
	_, err = fd.WriteString("\n# Counts analysis\n\n")
	if err != nil {
		return err
	}
	for _, rank := range ranks {
		_, err := fd.WriteString(fmt.Sprintf("-> Sub-communicator led by rank %d:\n", rank))
		if err != nil {
			return err
		}
		for _, b := range stats[rank].Bins {
			if b.Max != -1 {
				_, err := fd.WriteString(fmt.Sprintf("\t%d of the messages are of size between %d and %d bytes\n", b.Size, b.Min, b.Max-1))
				if err != nil {
					return err
				}
			} else {
				_, err := fd.WriteString(fmt.Sprintf("\t%d of messages are larger or equal of %d bytes\n", b.Size, b.Min))
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// GetCallData extract all the data related to a specific call.
func GetCallData(dir string, jobid int, rank int, callNum int, msgSizeThreshold int) (CallInfo, error) {
	var info CallInfo
	info.ID = callNum

	// Load the counts from raw data
	log.Printf("Extracting send/receive counts for call #%d\n", callNum)
	sendCountsFile, recvCountsFile := counts.GetFiles(jobid, rank)
	sendCountsFile = filepath.Join(dir, sendCountsFile)
	recvCountsFile = filepath.Join(dir, recvCountsFile)

	sendCountsFd, err := os.Open(sendCountsFile)
	if err != nil {
		return info, nil
	}
	defer sendCountsFd.Close()
	sendCountsFileReader := bufio.NewReader(sendCountsFd)

	recvCountsFd, err := os.Open(recvCountsFile)
	if err != nil {
		return info, nil
	}
	defer recvCountsFd.Close()
	recvCountsFileReader := bufio.NewReader(recvCountsFd)

	info.CountsData.CommSize, info.CountsData.SendData.Statistics.DatatypeSize, info.CountsData.SendData.Counts, err = counts.LookupCallFromFile(sendCountsFileReader, callNum)
	if err != nil {
		return info, nil
	}
	_, info.CountsData.RecvData.Statistics.DatatypeSize, info.CountsData.RecvData.Counts, err = counts.LookupCallFromFile(recvCountsFileReader, callNum)
	if err != nil {
		return info, nil
	}

	info.SendStats, err = counts.AnalyzeCounts(info.CountsData.SendData.Counts, msgSizeThreshold, info.CountsData.SendData.Statistics.DatatypeSize)
	if err != nil {
		return info, err
	}
	info.RecvStats, err = counts.AnalyzeCounts(info.CountsData.RecvData.Counts, msgSizeThreshold, info.CountsData.RecvData.Statistics.DatatypeSize)
	if err != nil {
		return info, err
	}

	// Get timings from formatted timing file
	// todo: if the files do not exist, we should get the data from scratch

	log.Printf("Extracting timings for call #%d\n", callNum)
	info.Timings, err = timings.GetCallData(dir, jobid, rank, callNum)
	if err != nil {
		return info, err
	}
	//info.AlltoallvTimings, info.LateArrivalTiming

	// Load patterns from result file.
	// todo: if the file does not exists, we should get the data from scratch
	log.Printf("Extracting patterns for call #%d\n", callNum)
	info.PatternStr, err = patterns.GetCall(dir, jobid, rank, callNum)
	if err != nil {
		return info, err
	}

	// Load the backtrace
	log.Printf("Extracting backtrace for call #%d\n", callNum)
	info.Backtrace, err = backtraces.GetCall(dir, callNum)
	if err != nil {
		return info, err
	}

	return info, nil
}

func Handle(input string) error {
	a := analyzer.CreateAnalyzer()
	a.InputFile = input

	err := a.Parse()
	if err != nil {
		return err
	}

	a.Finalize()

	return nil
}

func SaveStats(info OutputFileInfo, cs counts.SendRecvStats, patternsData patterns.Data, numCalls int, sizeThreshold int) error {
	_, err := info.defaultFd.WriteString(fmt.Sprintf("Total number of alltoallv calls: %d\n\n", numCalls))
	if err != nil {
		return err
	}

	err = counts.WriteDatatypeToFile(info.defaultFd, numCalls, cs.DatatypesSend, cs.DatatypesRecv)
	if err != nil {
		return err
	}

	err = counts.WriteCommunicatorSizesToFile(info.defaultFd, numCalls, cs.CommSizes)
	if err != nil {
		return err
	}

	err = counts.WriteCountStatsToFile(info.defaultFd, numCalls, sizeThreshold, cs)
	if err != nil {
		return err
	}

	_, err = info.patternsFd.WriteString("# Patterns\n")
	if err != nil {
		return err
	}
	num := 0
	for _, cp := range patternsData.AllPatterns {
		err = patterns.WriteToFile(info.patternsFd, num, numCalls, cp)
		if err != nil {
			return err
		}
		num++
	}

	if !patterns.NoSummary(patternsData) {
		if len(patternsData.OneToN) != 0 {
			_, err := info.patternsSummaryFd.WriteString("# 1 to N patterns\n\n")
			if err != nil {
				return err
			}
			num = 0
			for _, cp := range patternsData.OneToN {
				err = patterns.WriteToFile(info.patternsSummaryFd, num, numCalls, cp)
				if err != nil {
					return err
				}
				num++
			}
		}

		if len(patternsData.NToOne) != 0 {
			_, err := info.patternsSummaryFd.WriteString("\n# N to 1 patterns\n\n")
			if err != nil {
				return err
			}
			num = 0
			for _, cp := range patternsData.NToOne {
				err = patterns.WriteToFile(info.patternsSummaryFd, num, numCalls, cp)
				if err != nil {
					return err
				}
			}
		}

		if len(patternsData.NToN) != 0 {
			_, err := info.patternsSummaryFd.WriteString("\n# N to n patterns\n\n")
			if err != nil {
				return err
			}
			num = 0
			for _, cp := range patternsData.NToN {
				err = patterns.WriteToFile(info.patternsSummaryFd, num, numCalls, cp)
				if err != nil {
					return err
				}
			}
		}
	} else {
		_, err = info.patternsSummaryFd.WriteString("Nothing special detected; no summary")
		if err != nil {
			return err
		}
	}

	return nil
}

func GetCountProfilerFileDesc(basedir string, jobid int, rank int) (OutputFileInfo, error) {
	var info OutputFileInfo
	var err error

	info.defaultOutputFile = datafilereader.GetStatsFilePath(basedir, jobid, rank)
	info.patternsOutputFile = patterns.GetFilePath(basedir, jobid, rank)
	info.patternsSummaryOutputFile = patterns.GetSummaryFilePath(basedir, jobid, rank)
	info.defaultFd, err = os.OpenFile(info.defaultOutputFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return info, fmt.Errorf("unable to create %s: %s", info.defaultOutputFile, err)
	}

	info.patternsFd, err = os.OpenFile(info.patternsOutputFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return info, fmt.Errorf("unable to create %s: %s", info.patternsOutputFile, err)
	}

	info.patternsSummaryFd, err = os.OpenFile(info.patternsSummaryOutputFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return info, fmt.Errorf("unable to create %s: %s", info.patternsSummaryOutputFile, err)
	}

	info.Cleanup = func() {
		info.defaultFd.Close()
		info.patternsFd.Close()
		info.patternsSummaryFd.Close()
	}

	fmt.Println("Results are saved in:")
	fmt.Printf("-> %s\n", info.defaultOutputFile)
	fmt.Printf("-> %s\n", info.patternsOutputFile)
	fmt.Printf("Patterns summary: %s\n", info.patternsSummaryOutputFile)

	return info, nil
}
