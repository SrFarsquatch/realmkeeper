package main

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestUpdateHelpersToRemove(t *testing.T) {
	containers := []updateHelperContainer{
		{ID: "ok-old", Names: []string{"/hsm-update-helper-100"}, State: "exited", Status: "Exited (0) 3 days ago", Created: 100},
		{ID: "ok-new", Names: []string{"/hsm-update-helper-300"}, State: "exited", Status: "Exited (0) 1 hour ago", Created: 300},
		{ID: "fail-old", Names: []string{"/hsm-update-helper-150"}, State: "exited", Status: "Exited (1) 2 days ago", Created: 150},
		{ID: "fail-new", Names: []string{"/hsm-update-helper-250"}, State: "exited", Status: "Exited (1) 1 day ago", Created: 250},
		{ID: "running", Names: []string{"/hsm-update-helper-400"}, State: "running", Status: "Up 10 seconds", Created: 400},
		{ID: "manager", Names: []string{"/realmkeeper"}, State: "exited", Status: "Exited (0) 1 hour ago", Created: 50},
		{ID: "other", Names: []string{"/not-an-hsm-update-helper-1"}, State: "exited", Status: "Exited (0)", Created: 60},
	}
	// Shortly after these were made, the newest failed helper and the running
	// one are kept.
	got := updateHelpersToRemove(containers, time.Unix(500, 0))
	sort.Strings(got)
	want := []string{"fail-old", "ok-new", "ok-old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remove = %v, want %v (keep the newest failed helper, never running or unrelated containers)", got, want)
	}
	// A day and an hour later, the failed helper has served its purpose and
	// the running one is stuck.
	got = updateHelpersToRemove(containers, time.Unix(400, 0).Add(25*time.Hour))
	sort.Strings(got)
	want = []string{"fail-new", "fail-old", "ok-new", "ok-old", "running"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("a day later remove = %v, want %v", got, want)
	}
}
