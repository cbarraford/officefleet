package domain

// Job is the closed set of jobs an agent performs and a skill belongs to.
// JobUnknown is the zero/default sentinel — an unset agent or skill reads as
// "unknown"; it is NOT a real, assignable job (excluded from Jobs and Valid).
// Add a real value to Jobs (and to the frontend JOBS const in
// web/src/api/types.ts) to introduce a new job.
type Job string

const (
	JobUnknown   Job = "unknown"
	JobDeveloper Job = "developer"
)

// Jobs is the canonical list of real, assignable jobs (JobUnknown excluded).
var Jobs = []Job{JobDeveloper}

// Valid reports whether j is a real, assignable job. The unknown sentinel and
// the empty zero value are both invalid.
func (j Job) Valid() bool {
	for _, v := range Jobs {
		if j == v {
			return true
		}
	}
	return false
}

// String maps the empty zero value to the unknown sentinel so an unset job
// surfaces as "unknown" wherever a Job is printed or rendered.
func (j Job) String() string {
	if j == "" {
		return string(JobUnknown)
	}
	return string(j)
}
