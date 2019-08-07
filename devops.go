package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
)

var verbose, noUpload bool
var semesterFilter bool
var port int

// Devops details
var devOpsAccount, devOpsProject, devOpsToken, devOpsRepo string

// Azure Storage
var azStorageAcc, azStorageKey string

const (
	defaultPrCount = 200
	maxPrCount     = 1000

	defaultEpicWitQuery = "0325c50f-3511-4266-a9fe-80b989492c76"
)

func main() {

	// Setup command line parsing
	flag.BoolVar(&verbose, "v", false, "Show verbose output")
	flag.BoolVar(&noUpload, "nu", false, "Do not upload generated data into Azure")
	flag.BoolVar(&semesterFilter, "sem", true, "Filter workitems not finished in this semester")
	flag.IntVar(&port, "port", 80, "Port where the http server will listen")

	flag.Parse()

	// Fetch the access stuff from environment
	devOpsAccount = os.Getenv("AZUREDEVOPS_ACCOUNT")
	devOpsProject = os.Getenv("AZUREDEVOPS_PROJECT")
	devOpsToken = os.Getenv("AZUREDEVOPS_TOKEN")
	devOpsRepo = os.Getenv("AZUREDEVOPS_REPO")
	azStorageAcc = os.Getenv("AZURE_STORAGE_ACCOUNT")
	azStorageKey = os.Getenv("AZURE_STORAGE_ACCESS_KEY")

	if len(devOpsAccount) == 0 || len(devOpsProject) == 0 || len(devOpsToken) == 0 || len(devOpsRepo) == 0 ||
		len(azStorageAcc) == 0 || len(azStorageKey) == 0 {
		fmt.Println("Environment not setup")
		os.Exit(1)
	}

	addr := fmt.Sprintf(":%v", port)
	fmt.Println("Starting to listen on", port)
	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/wit", witHandler)
	http.HandleFunc("/pr", prHandler)
	log.Fatal(http.ListenAndServe(addr, nil))

}

// ================================================================================================
// Workitem
func showWorkStats(acc, proj, token string, azStorageAcc, azStorageKey string, epicWitQuery string) (bytes.Buffer, error) {
	var buffer bytes.Buffer
	// Get the list of epics from a epic's only query
	if verbose {
		fmt.Printf("Fetching epics using query %v\n", epicWitQuery)
	}

	parentEpics, err := getEpics(acc, proj, token, epicWitQuery)
	if err != nil {
		return buffer, err
	}

	var epicStats []EpicStat

	// For each epic start a go-routine and fetch all workitems that are child of it
	var wg sync.WaitGroup
	m := &sync.Mutex{}
	for _, epic := range parentEpics {
		if verbose {
			fmt.Printf("Fetching epic %v: %v\n", epic.Id, epic.Title)
		}

		wg.Add(1)
		go func(epic int, m *sync.Mutex) {
			defer wg.Done()
			epicStat, err := getEpicStat(acc, proj, token, epic)
			m.Lock()
			defer m.Unlock()
			if verbose {
				fmt.Print(".")
			}
			if err != nil {
				fmt.Println("Error getting stat for epic", epic)
				return
			}

			epicStats = append(epicStats, epicStat)

		}(epic.Id, m)
	}

	wg.Wait()
	if verbose {
		fmt.Println() // to move to next line after the progress dots shown above
	}

	var maxBars float32 = 120.0
	var maxCount float32
	for _, e := range epicStats {
		count := float32(e.Done + e.InProgress + e.NotDone + e.Unknown)
		if count > maxCount {
			maxCount = count
		}
	}

	for _, e := range epicStats {
		str := fmt.Sprintf("%v: %v (%v)\n", e.Epic.Id, e.Epic.Title, e.Epic.AssignedTo)
		buffer.WriteString(str)

		str = fmt.Sprintf("Done:%v InProgress:%v ToDo:%v Unknown:%v\n", e.Done, e.InProgress, e.NotDone, e.Unknown)
		buffer.WriteString(str)

		conv := maxBars / maxCount
		drawBars(&buffer, '#', conv*float32(e.Done))
		drawBars(&buffer, '=', conv*float32(e.InProgress))
		drawBars(&buffer, '-', conv*float32(e.NotDone))
		drawBars(&buffer, '.', conv*float32(e.Unknown))
		buffer.WriteString("\n\n")
	}

	fileName := "epicstat.png"
	err = saveWitStatImage(epicStats, fileName)
	if err != nil {
		return buffer, err
	}

	if !noUpload {
		url, err := uploadImageToAzure(azStorageAcc, azStorageKey, fileName)
		if err != nil {
			return buffer, err
		}

		if verbose {
			fmt.Println("Uploaded to", url)
		}
	}

	return buffer, nil
}

func getEpics(acc, proj, token, queryID string) ([]WorkItem, error) {
	q := NewWork(acc, proj, token)
	epics, err := q.GetWorkitems(queryID)
	if err != nil {
		return nil, err
	}

	return epics, nil
}

func getEpicStat(acc, proj, token string, parentEpic int) (EpicStat, error) {
	q := NewWork(acc, proj, token)

	stats, err := q.RefreshWit(parentEpic, semesterFilter)

	return stats, err
}

func drawBars(buffer *bytes.Buffer, ch rune, count float32) {
	for i := 0; i < int(count); i++ {
		buffer.WriteRune(ch)
	}
}

// ================================================================================================
// PR
func showPrStats(acc, proj, token, repo string, count int, azStorageAcc, azStorageKey string) (bytes.Buffer, error) {
	r := NewRepo(acc, proj, token, repo)
	var buffer bytes.Buffer
	if r.err != nil {
		return buffer, r.err
	}

	// Fetch PRs
	revStats, max := r.GetPullRequestReviewsByUser(count)
	barmax := float32(80.0)

	// Output!!
	if verbose {
		buffer.WriteString("\nReviewer Stats\n\n")
	}
	for _, revStat := range revStats {
		bar := int((barmax / float32(max)) * float32(revStat.Count))
		percentage := float32(revStat.Count) / float32(count) * 100.0
		buffer.WriteString(fmt.Sprintf("%30s %4d (%4.1f%%) ", revStat.Name, revStat.Count, percentage))
		buffer.WriteString("[")
		i := 0
		for ; i < bar; i++ {
			buffer.WriteRune('#')
		}

		for ; i < int(barmax); i++ {
			buffer.WriteRune('-')
		}
		buffer.WriteString("]\n")
	}

	fileName := "revstat.png"
	err := savePrStatImage(revStats, count, fileName)

	if err != nil {
		return buffer, err
	}

	if !noUpload {
		url, err := uploadImageToAzure(azStorageAcc, azStorageKey, fileName)
		if err != nil {
			return buffer, err
		}

		if verbose {
			fmt.Println("Uploaded to", url)
		}
	}

	return buffer, nil
}

// ================================================================================================
// Http
func rootHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Called!!!!")
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Welcome to DevOps tools from @abhinaba\nUse /pr and /wit\n"))
}

func prHandler(w http.ResponseWriter, r *http.Request) {
	prCount, err := getIntQueryParam("count", w, r, defaultPrCount)
	if err != nil {
		fmt.Printf("Error!! %v %v\n", r.URL, err)
		return
	}

	if prCount > maxPrCount || prCount <= 0 {
		writeError(w, "Invalid count range")
		return
	}

	buffer, err1 := showPrStats(devOpsAccount, devOpsProject, devOpsToken, devOpsRepo, prCount, azStorageAcc, azStorageKey)
	if err1 != nil {
		str := fmt.Sprintf("Error fetching pull-request stats: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(str))
	}

	buffer.WriteString(fmt.Sprintf("Processed %v pull-requests\n", prCount))
	w.WriteHeader(http.StatusOK)
	w.Write(buffer.Bytes())
}

func witHandler(w http.ResponseWriter, r *http.Request) {
	queryId, _ := getStringQueryParam("queryid", w, r, defaultEpicWitQuery)
	buffer, err := showWorkStats(devOpsAccount, devOpsProject, devOpsToken, azStorageAcc, azStorageKey, queryId)
	if err != nil {
		str := fmt.Sprintf("Error fetching work stats: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(str))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(buffer.Bytes())

}

func getIntQueryParam(name string, w http.ResponseWriter, r *http.Request, defaultValue int) (int, error) {
	i := defaultValue

	if keys, ok := r.URL.Query()[name]; ok {
		if len(keys) > 0 {
			var err error
			i, err = strconv.Atoi(keys[0])
			if err != nil {
				writeError(w, "Integer param count expected")
				return i, fmt.Errorf("Integer param count expected")
			}
		}
	}
	return i, nil
}

func getStringQueryParam(name string, w http.ResponseWriter, r *http.Request, defaultValue string) (string, error) {
	str := defaultValue

	if keys, ok := r.URL.Query()[name]; ok {
		if len(keys) > 0 {
			str = keys[0]
		}
	}
	return str, nil
}

func writeError(w http.ResponseWriter, message string) {
	w.WriteHeader(http.StatusBadRequest)
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(message))
}
