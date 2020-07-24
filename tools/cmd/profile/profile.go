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
	"reflect"
	"sort"
	"strings"

	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/bins"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/counts"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/datafilereader"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/format"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/maps"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/patterns"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/profiler"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/progress"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/timer"
	"github.com/gvallee/alltoallv_profiling/tools/internal/pkg/timings"
	"github.com/gvallee/go_util/pkg/util"
)

func analyzeJobRankCounts(basedir string, jobid int, rank int, sizeThreshold int, listBins []int) (map[int]*counts.CallData, counts.SendRecvStats, patterns.Data, error) {
	var p patterns.Data
	var sendRecvStats counts.SendRecvStats
	var cs map[int]*counts.CallData
	sendCountFile, recvCountFile := counts.GetFiles(jobid, rank)
	sendCountFile = filepath.Join(basedir, sendCountFile)
	recvCountFile = filepath.Join(basedir, recvCountFile)

	numCalls, err := counts.GetNumCalls(sendCountFile)
	if err != nil {
		return nil, sendRecvStats, p, fmt.Errorf("unable to get the number of alltoallv calls: %s", err)
	}

	// Note that by extracting the patterns, it will implicitly parses the send/recv counts
	// since it is necessary to figure out patterns.
	cs, p, err = patterns.ParseFiles(sendCountFile, recvCountFile, numCalls, rank, sizeThreshold)
	if err != nil {
		return cs, sendRecvStats, p, fmt.Errorf("unable to parse count file %s: %s", sendCountFile, err)
	}

	b := progress.NewBar(len(cs), "Bin creation")
	defer progress.EndBar(b)
	for _, callData := range cs {
		b.Increment(1)
		callData.SendData.BinThresholds = listBins
		sendBins := bins.Create(listBins)
		sendBins, err = bins.GetFromCounts(callData.SendData.Counts, sendBins, callData.SendData.Statistics.TotalNumCalls, callData.SendData.Statistics.DatatypeSize)
		if err != nil {
			return cs, sendRecvStats, p, err
		}
		err = bins.Save(basedir, jobid, rank, sendBins)
		if err != nil {
			return cs, sendRecvStats, p, err
		}
	}

	sendRecvStats, err = counts.GatherStatsFromCallData(cs, sizeThreshold)
	if err != nil {
		return cs, sendRecvStats, p, err
	}

	outputFilesInfo, err := profiler.GetCountProfilerFileDesc(basedir, jobid, rank)
	defer outputFilesInfo.Cleanup()

	err = profiler.SaveStats(outputFilesInfo, sendRecvStats, p, numCalls, sizeThreshold)
	if err != nil {
		return cs, sendRecvStats, p, fmt.Errorf("unable to save counters' stats: %s", err)
	}

	return cs, sendRecvStats, p, nil
}

func analyzeCountFiles(basedir string, sendCountFiles []string, recvCountFiles []string, sizeThreshold int, listBins []int) (map[int]counts.SendRecvStats, map[int]patterns.Data, error) {
	// Find all the files based on the rank who created the file.
	// Remember that we have more than one rank creating files, it means that different communicators were
	// used to run the alltoallv operations
	sendRanks, err := datafilereader.GetRanksFromFileNames(sendCountFiles)
	if err != nil || len(sendRanks) == 0 {
		return nil, nil, err
	}
	sort.Ints(sendRanks)

	recvRanks, err := datafilereader.GetRanksFromFileNames(recvCountFiles)
	if err != nil || len(recvRanks) == 0 {
		return nil, nil, err
	}
	sort.Ints(recvRanks)

	if !reflect.DeepEqual(sendRanks, recvRanks) {
		return nil, nil, fmt.Errorf("list of ranks logging send and receive counts differ, data likely to be corrupted")
	}

	sendJobids, err := datafilereader.GetJobIDsFromFileNames(sendCountFiles)
	if err != nil {
		return nil, nil, err
	}

	if len(sendJobids) != 1 {
		return nil, nil, fmt.Errorf("more than one job detected through send counts files; inconsistent data? (len: %d)", len(sendJobids))
	}

	recvJobids, err := datafilereader.GetJobIDsFromFileNames(recvCountFiles)
	if err != nil {
		return nil, nil, err
	}

	if len(recvJobids) != 1 {
		return nil, nil, fmt.Errorf("more than one job detected through recv counts files; inconsistent data?")
	}

	if sendJobids[0] != recvJobids[0] {
		return nil, nil, fmt.Errorf("results seem to be from different jobs, we strongly encourage users to get their counts data though a single run")
	}

	jobid := sendJobids[0]
	allStats := make(map[int]counts.SendRecvStats)
	allPatterns := make(map[int]patterns.Data)

	for _, rank := range sendRanks {
		_, sendRecvStats, p, err := analyzeJobRankCounts(basedir, jobid, rank, sizeThreshold, listBins)
		if err != nil {
			return nil, nil, err
		}
		/*
			sendRecvStats, err := counts.GatherStatsFromCallData(cs, sizeThreshold)
			if err != nil {
				return allStats, allPatterns, err
			}
		*/
		allStats[rank] = sendRecvStats
		allPatterns[rank] = p
	}

	return allStats, allPatterns, nil
}

func handleCountsFiles(dir string, sizeThreshold int, listBins []int) (map[int]counts.SendRecvStats, map[int]patterns.Data, error) {
	// Figure out all the send/recv counts files
	f, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}

	var profileFiles []string
	var sendCountsFiles []string
	var recvCountsFiles []string
	for _, file := range f {
		if strings.HasPrefix(file.Name(), format.ProfileSummaryFilePrefix) {
			profileFiles = append(profileFiles, filepath.Join(dir, file.Name()))
		}

		if strings.HasPrefix(file.Name(), counts.SendCountersFilePrefix) {
			sendCountsFiles = append(sendCountsFiles, filepath.Join(dir, file.Name()))
		}

		if strings.HasPrefix(file.Name(), counts.RecvCountersFilePrefix) {
			recvCountsFiles = append(recvCountsFiles, filepath.Join(dir, file.Name()))
		}
	}

	// Analyze all the files we found
	return analyzeCountFiles(dir, sendCountsFiles, recvCountsFiles, sizeThreshold, listBins)
}

func analyzeTimingsFiles(dir string, files []string) error {
	bar := progress.NewBar(len(files), "Handling timings files")
	defer progress.EndBar(bar)
	for _, file := range files {
		bar.Increment(1)
		// The output directory is where the data is, this tool keeps all the data together
		err := timings.ParseFile(file, dir)
		if err != nil {
			return err
		}
	}
	return nil
}

func handleTimingFiles(dir string) error {
	// Figure out all the send/recv counts files
	f, err := ioutil.ReadDir(dir)
	if err != nil {
		return err
	}

	var timingsFiles []string
	for _, file := range f {
		if strings.HasPrefix(file.Name(), timings.FilePrefix) {
			timingsFiles = append(timingsFiles, filepath.Join(dir, file.Name()))
		}
	}

	// Analyze all the files we found
	err = analyzeTimingsFiles(dir, timingsFiles)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	verbose := flag.Bool("v", false, "Enable verbose mode")
	dir := flag.String("dir", "", "Where all the data is")
	help := flag.Bool("h", false, "Help message")
	sizeThreshold := flag.Int("size-threshold", 200, "Size to differentiate small and big messages")
	binThresholds := flag.String("bins", "200,1024,2048,4096", "Comma-separated list of thresholds to use for the creation of bins")

	flag.Parse()

	cmdName := filepath.Base(os.Args[0])
	if *help {
		fmt.Printf("%s analyzes all the data gathered while running an application with our shared library", cmdName)
		fmt.Println("\nUsage:")
		flag.PrintDefaults()
	}

	logFile := util.OpenLogFile("alltoallv", cmdName)
	defer logFile.Close()
	if *verbose {
		nultiWriters := io.MultiWriter(os.Stdout, logFile)
		log.SetOutput(nultiWriters)
	} else {
		log.SetOutput(ioutil.Discard)
	}

	listBins := bins.GetFromInputDescr(*binThresholds)

	totalNumSteps := 4
	currentStep := 1
	fmt.Printf("* Step %d/%d: analyzing counts...\n", currentStep, totalNumSteps)
	t := timer.Start()
	stats, allPatterns, err := handleCountsFiles(*dir, *sizeThreshold, listBins)
	duration := t.Stop()
	if err != nil {
		fmt.Printf("ERROR: unable to analyze counts: %s\n", err)
		os.Exit(1)
	}
	fmt.Printf("Step completed in %s\n", duration)
	currentStep++

	fmt.Printf("\n* Step %d/%d: analyzing timing files...\n", currentStep, totalNumSteps)
	t = timer.Start()
	err = handleTimingFiles(*dir)
	duration = t.Stop()
	if err != nil {
		fmt.Printf("ERROR: unable to analyze timings: %s\n", err)
		os.Exit(1)
	}
	fmt.Printf("Step completed in %s\n", duration)
	currentStep++

	fmt.Printf("\n* Step %d/%d: analyzing MPI communicator data... ", currentStep, totalNumSteps)
	t = timer.Start()
	err = profiler.AnalyzeSubCommsResults(*dir, stats, allPatterns)
	duration = t.Stop()
	if err != nil {
		fmt.Printf("ERROR: unable to analyze sub-communicators results: %s\n", err)
		os.Exit(1)
	}
	fmt.Printf("Step completed in %s\n", duration)
	currentStep++

	/* todo: move creation of heat maps to analyzeCountFiles so we do not need to parse the count files again */
	fmt.Printf("\n* Step %d/%d: create maps... ", currentStep, totalNumSteps)
	t = timer.Start()
	err = maps.Create(maps.Heat, *dir)
	duration = t.Stop()
	if err != nil {
		fmt.Printf("ERROR: unable to create heat map: %s\n", err)
		os.Exit(1)
	}
	fmt.Printf("Step completed in %s\n", duration)
	currentStep++
}
