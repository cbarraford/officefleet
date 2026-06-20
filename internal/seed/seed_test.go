package seed

import (
	"context"
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

type fakeSkillSeeder struct {
	existing []*domain.Skill
	upserts  int
}

func (f *fakeSkillSeeder) UpsertByName(_ context.Context, d *domain.Skill) error {
	f.upserts++
	return nil
}
func (f *fakeSkillSeeder) List(_ context.Context) ([]*domain.Skill, error) { return f.existing, nil }

type fakeAssignmentSeeder struct {
	existing []*domain.Assignment
	upserts  int
}

func (f *fakeAssignmentSeeder) UpsertByAgentAndSkill(_ context.Context, a *domain.Assignment) error {
	f.upserts++
	return nil
}
func (f *fakeAssignmentSeeder) List(_ context.Context) ([]*domain.Assignment, error) {
	return f.existing, nil
}

func seedCfg() *config.Config {
	return &config.Config{
		Agents:      []config.AgentConfig{{Name: "a1"}},
		Skills:      []config.SkillConfig{{Name: "d1"}},
		Assignments: []config.AssignmentConfig{{Agent: "a1", Skill: "d1"}},
	}
}

func TestSeed_EmptyDBSeeds(t *testing.T) {
	ag, du, as := &fakeAgentSeeder{}, &fakeSkillSeeder{}, &fakeAssignmentSeeder{}
	if err := FromConfig(context.Background(), seedCfg(), ag, du, as, false); err != nil {
		t.Fatal(err)
	}
	if ag.upserts != 1 || du.upserts != 1 || as.upserts != 1 {
		t.Errorf("upserts = %d/%d/%d, want 1/1/1", ag.upserts, du.upserts, as.upserts)
	}
}

func TestSeed_PopulatedDBSkips(t *testing.T) {
	ag := &fakeAgentSeeder{existing: []*domain.Agent{{Name: "existing"}}}
	du, as := &fakeSkillSeeder{}, &fakeAssignmentSeeder{}
	if err := FromConfig(context.Background(), seedCfg(), ag, du, as, false); err != nil {
		t.Fatal(err)
	}
	if ag.upserts+du.upserts+as.upserts != 0 {
		t.Errorf("populated DB must not be reseeded; upserts = %d/%d/%d", ag.upserts, du.upserts, as.upserts)
	}
}

func TestSeed_ForceOverwrites(t *testing.T) {
	ag := &fakeAgentSeeder{existing: []*domain.Agent{{Name: "existing"}}}
	du, as := &fakeSkillSeeder{}, &fakeAssignmentSeeder{}
	if err := FromConfig(context.Background(), seedCfg(), ag, du, as, true); err != nil {
		t.Fatal(err)
	}
	if ag.upserts != 1 {
		t.Errorf("force must seed; agent upserts = %d", ag.upserts)
	}
}
