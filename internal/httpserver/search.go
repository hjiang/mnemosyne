package httpserver

import (
	"net/http"

	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/search"
)

func (s *Server) searchHandler(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	queryStr := r.URL.Query().Get("q")

	if queryStr == "" {
		s.render(w, "search.html", map[string]any{
			"Title": "Search",
			"Hint":  true,
		})
		return
	}

	q, err := search.Parse(queryStr)
	if err != nil {
		s.render(w, "search.html", map[string]any{
			"Title": "Search",
			"Error": err.Error(),
			"Query": queryStr,
		})
		return
	}

	results, err := s.search.Search(q, userID)
	if err != nil {
		s.render(w, "search.html", map[string]any{
			"Title": "Search",
			"Error": "Search failed: " + err.Error(),
			"Query": queryStr,
		})
		return
	}

	s.render(w, "search.html", map[string]any{
		"Title":   "Search",
		"Query":   queryStr,
		"Results": results,
		"Count":   len(results),
	})
}
