package schedulerapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
)

func indexHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Job Scheduler API v1.1")
}

// createJobHandler produces a new job by splitting the uploaded file
func createJobHandler(w http.ResponseWriter, r *http.Request) {

	batchFlag := r.URL.Query().Get("batch")
	useBatch := len(batchFlag) > 0 && batchFlag == "1"

	log.Infof("Received request to create job, content-size: %d, content-type is %s, use batch: %s", r.ContentLength, r.Header.Get("Content-Type"), strconv.FormatBool(useBatch))

	date := time.Now()
	jobUniqueID := randomString()
	jobID := fmt.Sprintf("%s-%s", date.Format("2006-01"), jobUniqueID)
	blobNamePrefix := fmt.Sprintf("%s/%s", date.Format("2006-01"), jobUniqueID)

	locationsCount, err := createJobInputFiles(r, *containerName, blobNamePrefix, NewInputSplitterByLine(*itemsPerJob))
	if err != nil {
		responseWithError(w, err)
		return
	}

	// start job based on input and amount of files created
	log.Printf("Finished creating file, found %d locations", locationsCount)

	if locationsCount < 1 {
		responseWithApplicationError(w, http.StatusBadRequest, "Input file generated 0 jobs")
		return
	}

	if useBatch {
		_, err = createBatchJob(jobID, *containerName, blobNamePrefix, locationsCount)
		if err != nil {
			responseWithError(w, err)
			return
		}
	} else {
		_, err = createKubernetesJob(jobID, *containerName, blobNamePrefix, locationsCount)
		if err != nil {
			responseWithError(w, err)
			return
		}
	}

	w.WriteHeader(202)
	w.Write([]byte(jobID))
}

// getJobHandler returns the job status
func getJobHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	jobID, ok := vars["id"]
	if !ok {
		responseWithApplicationError(w, http.StatusBadRequest, "Missing job id")
		return
	}

	k8sScheduler, err := createScheduler()
	if err != nil {
		responseWithError(w, err)
		return
	}

	jobs, err := k8sScheduler.SearchJobs(jobID, CreatedByLabelName, CreatedByLabelValue)
	if err != nil {
		responseWithError(w, err)
		return
	}

	if len(jobs.Items) == 0 {
		responseWithApplicationError(w, http.StatusNotFound, "Job not found")
		return
	}

	mainJob := jobs.Items[0]

	// jobHasWatcher, _ := strconv.ParseBool(mainJob.Labels[JobHasWatcherLabelName])

	// var watcherJob batchv1.Job
	// if jobHasWatcher {
	// 	watcherJobs, err := k8sScheduler.SearchJobs(getWatcherJobID(mainJob.GetName()), CreatedByLabelName, CreatedByLabelValue)

	// 	if err != nil {
	// 		responseWithError(w, err)
	// 		return
	// 	}

	// 	if len(watcherJobs.Items) > 0 {
	// 		watcherJob = watcherJobs.Items[0]
	// 	}
	// }

	jobStatus := NewJob(mainJob)

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(jobStatus)
	if err != nil {
		responseWithError(w, err)
		return
	}
}

// listJobsHandler returns the all jobs
func listJobsHandler(w http.ResponseWriter, r *http.Request) {

	k8sScheduler, err := createScheduler()
	if err != nil {
		responseWithError(w, err)
		return
	}

	allJobs, err := k8sScheduler.SearchJobs("", CreatedByLabelName, CreatedByLabelValue)
	if err != nil {
		responseWithError(w, err)
		return
	}

	result := make([]Job, 0)

	for _, job := range allJobs.Items {
		result = append(result, NewJob(job))
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		responseWithError(w, err)
		return
	}
}

// deleteJobHandler removes a job
func deleteJobHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	jobID, ok := vars["id"]
	if !ok {
		responseWithApplicationError(w, http.StatusBadRequest, "Missing job id")
		return
	}

	k8sScheduler, err := createScheduler()
	if err != nil {
		responseWithError(w, err)
		return
	}

	err = k8sScheduler.DeleteJob(jobID)
	if err != nil {
		responseWithError(w, err)
		return
	}

	w.WriteHeader(202)
}

// getJobResultHandler returns the job result
func getJobResultHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	jobID, ok := vars["id"]
	if !ok {
		responseWithApplicationError(w, http.StatusBadRequest, "Missing job id")
		return
	}

	k8sScheduler, err := createScheduler()
	if err != nil {
		responseWithError(w, err)
		return
	}

	jobs, err := k8sScheduler.SearchJobs(jobID, CreatedByLabelName, CreatedByLabelValue)
	if err != nil {
		responseWithError(w, err)
		return
	}

	if len(jobs.Items) == 0 {
		responseWithApplicationError(w, http.StatusNotFound, "Job not found")
		return
	}

	job := NewJob(jobs.Items[0])
	if job.Completions != job.Succeeded {
		responseWithApplicationError(w, http.StatusNotFound, fmt.Sprintf("Job has not yet completed: %d of %d completed", job.Succeeded, job.Completions))
		return
	}

	part := 0
	partQueryParam := r.URL.Query().Get("part")
	if len(partQueryParam) > 0 {
		part, err = strconv.Atoi(partQueryParam)
		if err != nil {
			part = 0
		}
	}

	if part > job.Parts {
		responseWithApplicationError(w, http.StatusBadRequest, fmt.Sprintf("Part has invalid value. Job has %d parts, requested for part %d", job.Parts, part))
		return
	}

	// open files and stream back
	w.WriteHeader(http.StatusOK)
	w.Header().Add("Content-Type", "application/json")
	err = writeAzureBlobs(r.Context(), w, job.StorageContainer, job.StorageBlobPrefix, part)
	if err != nil {
		responseWithError(w, err)
		return
	}
}
