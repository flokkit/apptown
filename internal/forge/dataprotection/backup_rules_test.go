package dataprotection

import (
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
)

func TestBackupGoodCopy(t *testing.T) {
	b := &Backup{Items: []BackupItem{
		{Kind: "volume", Name: "data", Error: "tar failed"},
		{Kind: "need", Name: "postgres", File: "postgres.sql"},
	}}
	if got := b.GoodCopy("volume", "data"); got != nil {
		t.Fatalf("a failed copy was offered for restore: %+v", got)
	}
	if got := b.GoodCopy("need", "postgres"); got == nil || got.File != "postgres.sql" {
		t.Fatalf("the good copy was not found: %+v", got)
	}
	if got := b.GoodCopy("need", "redis"); got != nil {
		t.Fatalf("an item the set never held was offered: %+v", got)
	}
}

func TestBackupSafetyCopyDoesNotPrune(t *testing.T) {
	if !(&Backup{Trigger: TriggerPreRestore}).SafetyCopy() {
		t.Fatal("a pre-restore set is a safety copy")
	}
	if (&Backup{Trigger: "manual"}).SafetyCopy() {
		t.Fatal("a manual set is not a safety copy")
	}
}

func TestTargetHoldsBackups(t *testing.T) {
	for _, kind := range []string{deployment.KindDockerHost, deployment.KindPortainer} {
		if err := (&deployment.Target{Kind: kind}).HoldsBackups(); err != nil {
			t.Fatalf("a %s target holds AppTown's backups: %v", kind, err)
		}
	}
	for _, kind := range []string{deployment.KindDOApp, deployment.KindS3Bucket} {
		if err := (&deployment.Target{Kind: kind}).HoldsBackups(); err == nil {
			t.Fatalf("a %s target keeps its own backups", kind)
		}
	}
}

func TestAttributesWithDefaults(t *testing.T) {
	got := deployment.Attributes{}.WithDefaults()
	if got.Feed != "promotion" || got.Audience != "team" || got.DataClass != "non-production" {
		t.Fatalf("defaults: %+v", got)
	}
	kept := deployment.Attributes{Feed: "landing", Audience: "public", DataClass: "production"}.WithDefaults()
	if kept != (deployment.Attributes{Feed: "landing", Audience: "public", DataClass: "production"}) {
		t.Fatalf("stated attributes were overridden: %+v", kept)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the defaults must validate: %v", err)
	}
}
