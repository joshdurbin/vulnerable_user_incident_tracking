package main

import (
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/search"
)

// searchClient returns the OpenSearch client, or nil when search is not configured. Building it does not
// connect, so an unreachable cluster never stops a process from starting.
func (a *app) searchClient() (*search.Client, error) {
	c := a.cfg.Search
	if c.URL == "" {
		return nil, nil
	}
	return search.New(search.Config{URL: c.URL, Username: c.Username, Password: c.Password, InsecureSkipVerify: c.InsecureSkipVerify, Replicas: c.Replicas})
}
