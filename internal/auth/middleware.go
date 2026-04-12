package auth

import (
	"encoding/hex"
	"errors"
	"net/http"
)

const cookieName = "mnemosyne_session"

// RequireAuth returns middleware that enforces authentication. Unauthenticated
// requests are redirected to loginURL. Invalid or expired cookies are cleared.
func RequireAuth(sessions *SessionStore, loginURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(cookieName)
			if err != nil {
				http.Redirect(w, r, loginURL, http.StatusSeeOther)
				return
			}

			id, err := hex.DecodeString(cookie.Value)
			if err != nil {
				clearSessionCookie(w)
				http.Redirect(w, r, loginURL, http.StatusSeeOther)
				return
			}

			sess, err := sessions.Lookup(id)
			if err != nil {
				clearSessionCookie(w)
				if errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrSessionNotFound) {
					http.Redirect(w, r, loginURL, http.StatusSeeOther)
					return
				}
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}

			ctx := WithUserID(r.Context(), sess.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SetSessionCookie writes a session cookie to the response.
func SetSessionCookie(w http.ResponseWriter, sessionID []byte) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    hex.EncodeToString(sessionID),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
