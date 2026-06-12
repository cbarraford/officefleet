package seed

import (
	"context"
	"fmt"
	"testing"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
)

type fakeAgentSeeder struct {
	existing []*domain.Agent
	upserts  int
}

func (f *fakeAgentSeeder) UpsertByName(_ context.Context, a *domain.Agent) error {
	f.upserts++
	return nil
}
func (f *fakeAgentSeeder) List(_ context.Context) ([]*domain.Agent, error) { return f.existing, nil }

type fakeDutySeeder struct {
	existing []*domain.Duty
	upserts  int
}

func (f *fakeDutySeeder) UpsertByName(_ context.Context, d *domain.Duty) error {
	f.upserts++
	return nil
}
func (f *fakeDutySeeder) List(_ context.Context) ([]*domain.Duty, error) { return f.existing, nil }

type fakeAssignmentSeeder struct {
	existing []*domain.Assignment
	upserts  int
	rows     map[string]*domain.Assignment
}

func (f *fakeAssignmentSeeder) UpsertByAgentDutyAndName(_ context.Context, a *domain.Assignment) error {
	f.upserts++
	if f.rows == nil {
		f.rows = map[string]*domain.Assignment{}
	}
	key := fmt.Sprintf("%s/%s/%s", a.AgentID, a.DutyID, a.Name)
	cp := *a
	f.rows[key] = &cp
	return nil
}
func (f *fakeAssignmentSeeder) List(_ context.Context) ([]*domain.Assignment, error) {
	return f.existing, nil
}

func seedCfg() *config.Config {
	return &config.Config{
		Agents:      []config.AgentConfig{{Name: "a1"}},
		Duties:      []config.DutyConfig{{Name: "d1"}},
		Assignments: []config.AssignmentConfig{{Agent: "a1", Duty: "d1"}},
	}
}

func TestSeed_EmptyDBSeeds(t *testing.T) {
	ag, du, as := &fakeAgentSeeder{}, &fakeDutySeeder{}, &fakeAssignmentSeeder{}
	if err := FromConfig(context.Background(), seedCfg(), ag, du, as, false); err != nil {
		t.Fatal(err)
	}
	if ag.upserts != 1 || du.upserts != 1 || as.upserts != 1 {
		t.Errorf("upserts = %d/%d/%d, want 1/1/1", ag.upserts, du.upserts, as.upserts)
	}
}

func TestSeed_PopulatedDBSkips(t *testing.T) {
	ag := &fakeAgentSeeder{existing: []*domain.Agent{{Name: "existing"}}}
	du, as := &fakeDutySeeder{}, &fakeAssignmentSeeder{}
	if err := FromConfig(context.Background(), seedCfg(), ag, du, as, false); err != nil {
		t.Fatal(err)
	}
	if ag.upserts+du.upserts+as.upserts != 0 {
		t.Errorf("populated DB must not be reseeded; upserts = %d/%d/%d", ag.upserts, du.upserts, as.upserts)
	}
}

func TestSeed_ForceOverwrites(t *testing.T) {
	ag := &fakeAgentSeeder{existing: []*domain.Agent{{Name: "existing"}}}
	du, as := &fakeDutySeeder{}, &fakeAssignmentSeeder{}
	if err := FromConfig(context.Background(), seedCfg(), ag, du, as, true); err != nil {
		t.Fatal(err)
	}
	if ag.upserts != 1 {
		t.Errorf("force must seed; agent upserts = %d", ag.upserts)
	}
}

func TestSeed_AssignmentNamesPreserveSameAgentDutyVariants(t *testing.T) {
	cfg := seedCfg()
	cfg.Duties[0].TriggerKinds = []string{"manual", "cron"}
	cfg.Assignments = []config.AssignmentConfig{
		{Name: "manual-review", Agent: "a1", Duty: "d1", Trigger: domain.TriggerConfig{Kind: "manual"}},
		{Name: "cron-review", Agent: "a1", Duty: "d1", Trigger: domain.TriggerConfig{Kind: "cron"}},
	}
	ag, du, as := &fakeAgentSeeder{}, &fakeDutySeeder{}, &fakeAssignmentSeeder{}
	if err := FromConfig(context.Background(), cfg, ag, du, as, false); err != nil {
		t.Fatal(err)
	}
	if len(as.rows) != 2 {
		t.Fatalf("seeded assignments = %d, want 2 distinct same-agent-duty rows", len(as.rows))
	}
	seen := map[string]bool{}
	for _, row := range as.rows {
		seen[row.Name] = true
	}
	if !seen["manual-review"] || !seen["cron-review"] {
		t.Fatalf("seeded assignment names = %v, want manual-review and cron-review", seen)
	}
}
