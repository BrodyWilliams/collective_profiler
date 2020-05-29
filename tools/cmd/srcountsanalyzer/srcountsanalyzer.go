//
// Copyright (c) 2020, NVIDIA CORPORATION. All rights reserved.
//
// See LICENSE.txt for license information
//

package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/notation"

	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/datafilereader"

	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/profiler"
	"github.com/gvallee/go_util/pkg/util"
)

type callPattern struct {
	send  map[int]int
	recv  map[int]int
	count int
	calls []int
}

type GlobalPatterns struct {
	cp []*callPattern
}

func (globalPatterns *GlobalPatterns) addPattern(callNum int, sendPatterns map[int]int, recvPatterns map[int]int) error {
	for idx, x := range globalPatterns.cp {
		if datafilereader.CompareCallPatterns(x.send, sendPatterns) && datafilereader.CompareCallPatterns(x.recv, recvPatterns) {
			// Increment count for pattern
			log.Printf("-> Alltoallv call #%d - Adding alltoallv to pattern %d...\n", callNum, idx)
			x.count++
			x.calls = append(x.calls, callNum)
			return nil
		}
	}

	// If we get here, it means that we did not find a similar pattern
	log.Printf("-> Alltoallv call %d - Adding new pattern...\n", callNum)
	new_cp := new(callPattern)
	new_cp.send = sendPatterns
	new_cp.recv = recvPatterns
	new_cp.count = 1
	new_cp.calls = append(new_cp.calls, callNum)
	globalPatterns.cp = append(globalPatterns.cp, new_cp)

	return nil
}

func displayPattern(p map[int]int, ctx string) {
	for numPeers, numRanks := range p {
		fmt.Printf("%d ranks are %s non-zero data to %d other ranks\n", numRanks, ctx, numPeers)
	}
}

func displayCallPatterns(info datafilereader.CallInfo) {
	for numPeers, numRanks := range info.Patterns.SendPatterns {
		fmt.Printf("%d ranks are sending non-zero data to %d other ranks\n", numRanks, numPeers)
	}
}

func main() {
	verbose := flag.Bool("v", false, "Enable verbose mode")
	dir := flag.String("dir", "", "Where all the data is")
	pid := flag.Int("pid", 0, "Identifier of the experiment, e.g., X from <pidX> in the profile file name")
	jobid := flag.Int("jobid", 0, "Job ID associated to the count files")
	sizeThreshold := flag.Int("size-threshold", 200, "Threshold to differentiate size and large messages")

	flag.Parse()

	logFile := util.OpenLogFile("alltoallv", "srcountsanalyzer")
	defer logFile.Close()
	if *verbose {
		nultiWriters := io.MultiWriter(os.Stdout, logFile)
		log.SetOutput(nultiWriters)
	} else {
		log.SetOutput(ioutil.Discard)
	}

	sendCountsFile, recvCountsFile := profiler.GetCountsFiles(*jobid, *pid)
	sendCountsFile = filepath.Join(*dir, sendCountsFile)
	recvCountsFile = filepath.Join(*dir, recvCountsFile)

	if !util.PathExists(sendCountsFile) || !util.PathExists(recvCountsFile) {
		log.Fatalf("unable to locate send or recv counts file(s) in %s", *dir)
	}

	log.Printf("Send counts file: %s\n", sendCountsFile)
	log.Printf("Recv counts file: %s\n", recvCountsFile)

	numCalls, err := datafilereader.GetNumCalls(sendCountsFile)
	if err != nil {
		log.Fatalf("unable to get the number of alltoallv calls: %s", err)
	}

	fmt.Printf("Total number of alltoallv calls: %d\n", numCalls)

	//a := analyzer.CreateSRCountsAnalyzer(sendCountsFile, recvCountsFile)

	/*
		fSendCounts, err := os.Open(sendCountsFile)
		if err != nil {
			log.Fatalf("unable to open %s: %s", sendCountsFile, err)
		}
		defer fSendCounts.Close()

		fRecvCounts, err := os.Open(recvCountsFile)
		if err != nil {
			log.Fatalf("unable to open %s: %s", sendCountsFile, err)
		}

		sendCountReader := bufio.NewReader(fSendCounts)
		recvCountReader := bufio.NewReader(fRecvCounts)
	*/

	var globalPatterns GlobalPatterns
	datatypesSend := make(map[int]int)
	datatypesRecv := make(map[int]int)
	commSizes := make(map[int]int)
	sendMins := make(map[int]int)
	recvMins := make(map[int]int)
	sendMaxs := make(map[int]int)
	recvMaxs := make(map[int]int)
	recvNotZeroMins := make(map[int]int)
	sendNotZeroMins := make(map[int]int)
	callSendSparsity := make(map[int]int)
	callRecvSparsity := make(map[int]int)

	numSendSmallMsgs := 0
	numSendSmallNotZeroMsgs := 0
	numSendLargeMsgs := 0

	for i := 0; i < numCalls; i++ {
		log.Printf("Analyzing call #%d\n", i)
		callInfo, err := datafilereader.LookupCall(sendCountsFile, recvCountsFile, i, *sizeThreshold)
		if err != nil {
			log.Fatalf("unable to lookup call #%d: %s", i, err)
		}

		numSendSmallMsgs += callInfo.SendSmallMsgs
		numSendSmallNotZeroMsgs += callInfo.SendSmallNotZeroMsgs
		numSendLargeMsgs += callInfo.SendLargeMsgs

		if _, ok := datatypesSend[callInfo.SendDatatypeSize]; ok {
			datatypesSend[callInfo.SendDatatypeSize]++
		} else {
			datatypesSend[callInfo.SendDatatypeSize] = 1
		}

		if _, ok := datatypesRecv[callInfo.RecvDatatypeSize]; ok {
			datatypesRecv[callInfo.RecvDatatypeSize]++
		} else {
			datatypesRecv[callInfo.RecvDatatypeSize] = 1
		}

		if _, ok := commSizes[callInfo.CommSize]; ok {
			commSizes[callInfo.CommSize]++
		} else {
			commSizes[callInfo.CommSize] = 1
		}

		if _, ok := sendMins[callInfo.SendMin]; ok {
			sendMins[callInfo.SendMin]++
		} else {
			sendMins[callInfo.SendMin] = 1
		}

		if _, ok := recvMins[callInfo.RecvMin]; ok {
			recvMins[callInfo.RecvMin]++
		} else {
			recvMins[callInfo.RecvMin] = 1
		}

		if _, ok := sendMaxs[callInfo.SendMax]; ok {
			sendMaxs[callInfo.SendMax]++
		} else {
			sendMaxs[callInfo.SendMax] = 1
		}

		if _, ok := recvMaxs[callInfo.RecvMax]; ok {
			recvMaxs[callInfo.RecvMax]++
		} else {
			recvMaxs[callInfo.RecvMax] = 1
		}

		if _, ok := sendNotZeroMins[callInfo.SendNotZeroMin]; ok {
			sendMins[callInfo.SendNotZeroMin]++
		} else {
			sendMins[callInfo.SendNotZeroMin] = 1
		}

		if _, ok := recvNotZeroMins[callInfo.RecvNotZeroMin]; ok {
			recvMins[callInfo.RecvNotZeroMin]++
		} else {
			recvMins[callInfo.RecvNotZeroMin] = 1
		}

		if _, ok := callSendSparsity[callInfo.TotalSendZeroCounts]; ok {
			callSendSparsity[callInfo.TotalSendZeroCounts]++
		} else {
			callSendSparsity[callInfo.TotalSendZeroCounts] = 1
		}

		if _, ok := callRecvSparsity[callInfo.TotalRecvZeroCounts]; ok {
			callRecvSparsity[callInfo.TotalRecvZeroCounts]++
		} else {
			callRecvSparsity[callInfo.TotalRecvZeroCounts] = 1
		}

		//displayCallPatterns(callInfo)
		// Analyze the send/receive pattern from the call
		err = globalPatterns.addPattern(i, callInfo.Patterns.SendPatterns, callInfo.Patterns.RecvPatterns)
		if err != nil {
			log.Fatalf("unabel to add pattern: %s", err)
		}
	}

	for datatypeSize, n := range datatypesSend {
		fmt.Printf("%d/%d calls use a datatype of size %d while sending data\n", n, numCalls, datatypeSize)
	}

	for datatypeSize, n := range datatypesRecv {
		fmt.Printf("%d/%d calls use a datatype of size %d while receiving data\n", n, numCalls, datatypeSize)
	}
	fmt.Printf("\n")

	for commSize, n := range commSizes {
		fmt.Printf("%d/%d calls use a communicator size of %d\n", n, numCalls, commSize)
	}
	fmt.Printf("\n")

	totalSendMsgs := numSendSmallMsgs + numSendLargeMsgs
	fmt.Printf("%d/%d of all messages are large (threshold = %d)\n", numSendLargeMsgs, totalSendMsgs, *sizeThreshold)
	fmt.Printf("%d/%d of all messages are small (threshold = %d)\n", numSendSmallMsgs, totalSendMsgs, *sizeThreshold)
	fmt.Printf("%d/%d of all messages are small, but not 0-size (threshold = %d)\n", numSendSmallNotZeroMsgs, totalSendMsgs, *sizeThreshold)

	fmt.Printf("\n# Sparsity\n")
	for numZeros, nCalls := range callSendSparsity {
		fmt.Printf("%d/%d of all calls have %d send counts equals to zero\n", nCalls, numCalls, numZeros)
	}
	for numZeros, nCalls := range callRecvSparsity {
		fmt.Printf("%d/%d of all calls have %d recv counts equals to zero\n", nCalls, numCalls, numZeros)
	}

	fmt.Printf("\n# Min/max\n")
	for mins, n := range sendMins {
		fmt.Printf("%d/%d calls have a send count min of %d\n", n, numCalls, mins)
	}
	for mins, n := range recvMins {
		fmt.Printf("%d/%d calls have a recv count min of %d\n", n, numCalls, mins)
	}

	for mins, n := range sendNotZeroMins {
		fmt.Printf("%d/%d calls have a send count min of %d (excluding zero)\n", n, numCalls, mins)
	}
	for mins, n := range recvNotZeroMins {
		fmt.Printf("%d/%d calls have a recv count min of %d (excluding zero)\n", n, numCalls, mins)
	}

	for maxs, n := range sendMaxs {
		fmt.Printf("%d/%d calls have a send count max of %d\n", n, numCalls, maxs)
	}
	for maxs, n := range recvMaxs {
		fmt.Printf("%d/%d calls have a recv count max of %d\n", n, numCalls, maxs)
	}

	fmt.Printf("\n# Patterns\n")
	num := 0
	for _, cp := range globalPatterns.cp {
		fmt.Printf("## Pattern #%d (%d alltoallv calls)\n", num, cp.count)
		fmt.Printf("Alltoallv calls: %s\n", notation.CompressIntArray(cp.calls))

		for sendTo, n := range cp.send {
			fmt.Printf("%d ranks sent to %d other ranks\n", n, sendTo)
		}
		for recvFrom, n := range cp.recv {
			fmt.Printf("%d ranks recv'd from %d other ranks\n", n, recvFrom)
		}

		fmt.Printf("\n")

		num++
	}
}
