package upstream

import "testing"

func TestWebsiteURLRepositoryAllSavePathsAndRestore(t *testing.T) {
	pool := singaporeCostPGPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	site := newTestSite("website-site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: websiteFixtureAPI, AccessToken: "fixture-access"})
	site.BaseURL = websiteFixtureAPI
	websiteSet(t, site, websiteFixtureURL)
	if err := repo.SaveSite(ctx, *site); err != nil {
		t.Fatal(err)
	}
	check := func(want string) Site {
		t.Helper()
		all, err := repo.ListSites(ctx)
		if err != nil || len(all) != 1 {
			t.Fatal("list/restore failed", err)
		}
		own, err := repo.ListSitesForUser(ctx, "user")
		if err != nil || len(own) != 1 {
			t.Fatal("user list failed", err)
		}
		for _, saved := range []Site{all[0], own[0]} {
			if websiteJSON(t, saved)["websiteUrl"] != want || saved.BaseURL != websiteFixtureAPI || saved.Session.BaseURL != websiteFixtureAPI {
				t.Fatal("SQL lost or mixed addresses")
			}
		}
		return all[0]
	}
	current := check(websiteFixtureURL)
	websiteSet(t, &current, "https://full-save.fixture.invalid/")
	if _, err := repo.SaveSiteWithSnapshotVersion(ctx, current); err != nil {
		t.Fatal(err)
	}
	current = check("https://full-save.fixture.invalid/")
	candidate := cloneSite(&current)
	websiteSet(t, &candidate, "https://conditional.fixture.invalid/")
	if ok, err := repo.SaveExistingSiteIfUnchanged(ctx, current, candidate); err != nil || !ok {
		t.Fatal("conditional save", err)
	}
	current = check("https://conditional.fixture.invalid/")
	candidate = cloneSite(&current)
	websiteSet(t, &candidate, "https://group-save.fixture.invalid/")
	if _, _, ok, _, err := repo.SaveExistingSiteWithGroupGuard(ctx, current, candidate, current); err != nil || !ok {
		t.Fatal("group guarded save", err)
	}
	current = check("https://group-save.fixture.invalid/")
	// Stale configuration must not overwrite a newer website in PostgreSQL.
	stale := current
	websiteSet(t, &stale, "https://stale.fixture.invalid/")
	if ok, err := repo.SaveExistingSiteIfUnchanged(ctx, stale, stale); err != nil || ok {
		t.Fatal("stale website accepted", err)
	}
	check("https://group-save.fixture.invalid/")
	cache := newFakeSiteCache()
	s := NewService(nil, repo, nil, cache)
	t.Cleanup(s.Close)
	s.SetBackgroundTasksDisabled(true)
	if err := s.RestoreSavedSites(ctx); err != nil {
		t.Fatal(err)
	}
	if websiteJSON(t, cache.sites[site.ID])["websiteUrl"] != "https://group-save.fixture.invalid/" {
		t.Fatal("restore lost website")
	}
	// An old row uses the schema default and never copies the API value.
	if _, err := pool.Exec(ctx, `UPDATE upstream_sites SET website_url=DEFAULT WHERE id=$1`, site.ID); err != nil {
		t.Fatal(err)
	}
	check("")
	if _, err := pool.Exec(ctx, `ALTER TABLE upstream_sites DROP COLUMN website_url`); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	check("")
	// A transaction failure keeps the previous website and API on the real row.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_website_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejected website update'; END; $$; CREATE TRIGGER reject_website BEFORE UPDATE ON upstream_sites FOR EACH ROW EXECUTE FUNCTION reject_website_test()`); err != nil {
		t.Fatal(err)
	}
	current = check("")
	candidate = cloneSite(&current)
	websiteSet(t, &candidate, websiteFixtureURL)
	if _, err := repo.SaveExistingSiteIfUnchanged(ctx, current, candidate); err == nil {
		t.Fatal("failed SQL update reported success")
	}
	check("")
}
