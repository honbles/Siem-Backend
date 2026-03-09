package api

import (
	"context"
	"net/http"

	"obsidianwatch/backend/internal/store"
)

type agentStore interface {
	ListAgents(ctx context.Context) ([]store.Agent, error)
	GetAgent(ctx context.Context, id string) (*store.Agent, error)
}

// GET /api/v1/agents  — list all known agents
func handleListAgents(db agentStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		agents, err := db.ListAgents(r.Context())
		if err != nil {
			http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"agents": agents,
			"count":  len(agents),
		})
	}
}

// GET /api/v1/agents/{id}  — get a single agent
func handleGetAgent(db agentStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		// Extract ID from path: /api/v1/agents/{id}
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, `{"error":"missing agent id"}`, http.StatusBadRequest)
			return
		}

		agent, err := db.GetAgent(r.Context(), id)
		if err != nil {
			http.Error(w, `{"error":"agent not found"}`, http.StatusNotFound)
			return
		}

		writeJSON(w, http.StatusOK, agent)
	}
}

// Ensure agentStore is satisfied by *store.DB at compile time.
var _ agentStore = (*store.DB)(nil)
