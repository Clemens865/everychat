package storage

import (
	"context"
	"errors"
	"testing"
)

func TestListBots_IncludesSeededDemo(t *testing.T) {
	db := openTestDB(t)
	bots, err := ListBots(context.Background(), db)
	if err != nil {
		t.Fatalf("ListBots: %v", err)
	}
	if len(bots) == 0 {
		t.Fatal("expected at least the seeded demo bot")
	}
	var found bool
	for _, b := range bots {
		if b.Name == "steuerkanzlei-demo" {
			found = true
			if b.EvalThreshold != 0.85 {
				t.Errorf("threshold: got %v, want 0.85", b.EvalThreshold)
			}
		}
	}
	if !found {
		t.Errorf("seeded steuerkanzlei-demo not in ListBots result: %+v", bots)
	}
}

func TestLoadBot_NotFound(t *testing.T) {
	db := openTestDB(t)
	if _, err := LoadBot(context.Background(), db, 999); !errors.Is(err, ErrBotNotFound) {
		t.Fatalf("got %v, want ErrBotNotFound", err)
	}
}

func TestUpdateDraftPrompt_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	bots, _ := ListBots(context.Background(), db)
	id := bots[0].ID

	if err := UpdateDraftPrompt(context.Background(), db, id, "neuer entwurf"); err != nil {
		t.Fatal(err)
	}
	b, err := LoadBot(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	if b.DraftPrompt != "neuer entwurf" {
		t.Fatalf("got %q", b.DraftPrompt)
	}
}

func TestPromoteDraftToPublished_CopiesAndStamps(t *testing.T) {
	db := openTestDB(t)
	bots, _ := ListBots(context.Background(), db)
	id := bots[0].ID

	if err := UpdateDraftPrompt(context.Background(), db, id, "veröffentlicht-werden"); err != nil {
		t.Fatal(err)
	}
	if err := PromoteDraftToPublished(context.Background(), db, id); err != nil {
		t.Fatal(err)
	}
	b, _ := LoadBot(context.Background(), db, id)
	if b.SystemPrompt != "veröffentlicht-werden" {
		t.Errorf("system_prompt: got %q", b.SystemPrompt)
	}
	if b.Status != "published" {
		t.Errorf("status: got %q", b.Status)
	}
	if !b.PublishedAt.Valid {
		t.Errorf("published_at not stamped")
	}
}

func TestSetCompliance_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	bots, _ := ListBots(context.Background(), db)
	id := bots[0].ID

	if err := SetCompliance(context.Background(), db, id, "https://x.de/datenschutz", "https://x.de/agb"); err != nil {
		t.Fatal(err)
	}
	b, _ := LoadBot(context.Background(), db, id)
	if !b.PrivacyPolicyURL.Valid || b.PrivacyPolicyURL.String != "https://x.de/datenschutz" {
		t.Errorf("privacy: %v", b.PrivacyPolicyURL)
	}
	if !b.AGBURL.Valid || b.AGBURL.String != "https://x.de/agb" {
		t.Errorf("agb: %v", b.AGBURL)
	}

	// Empty strings should null-out the columns.
	if err := SetCompliance(context.Background(), db, id, "", ""); err != nil {
		t.Fatal(err)
	}
	b, _ = LoadBot(context.Background(), db, id)
	if b.PrivacyPolicyURL.Valid || b.AGBURL.Valid {
		t.Errorf("expected nulls, got privacy=%v agb=%v", b.PrivacyPolicyURL, b.AGBURL)
	}
}
