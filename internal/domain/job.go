package domain

// Job is the closed set of jobs an agent performs and a skill belongs to.
// Add a value here (and to the frontend JOBS const in web/src/api/types.ts)
// to introduce a new job.
type Job string

const JobDeveloper Job = "developer"

// Jobs is the canonical list of valid jobs.
var Jobs = []Job{JobDeveloper}

// Valid reports whether j is a known job.
func (j Job) Valid() bool {
	for _, v := range Jobs {
		if j == v {
			return true
		}
	}
	return false
}
