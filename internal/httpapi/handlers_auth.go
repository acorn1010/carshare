package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"carshare/internal/auth"
)

// stateCookieName holds the OAuth anti-CSRF state between login and callback.
const stateCookieName = "carshare_oauth_state"

func (server *Server) handleGoogleLogin(writer http.ResponseWriter, request *http.Request) {
	state, err := auth.NewState()
	if err != nil {
		slog.Error("oauth state", slog.String("error", err.Error()))
		writeError(writer, http.StatusInternalServerError, "internal", "could not start sign-in")
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: stateCookieName, Value: state, Path: "/v1/auth",
		MaxAge:   int((10 * time.Minute).Seconds()),
		HttpOnly: true, Secure: server.params.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(writer, request, server.params.Google.AuthCodeURL(state), http.StatusFound)
}

func (server *Server) handleGoogleCallback(writer http.ResponseWriter, request *http.Request) {
	stateCookie, err := request.Cookie(stateCookieName)
	if err != nil || stateCookie.Value == "" || request.URL.Query().Get("state") != stateCookie.Value {
		writeError(writer, http.StatusBadRequest, "bad_state", "sign-in state mismatch, start over")
		return
	}
	code := request.URL.Query().Get("code")
	if code == "" {
		writeError(writer, http.StatusBadRequest, "bad_request", "missing code")
		return
	}
	profile, err := server.params.Google.FetchProfile(request.Context(), code)
	if err != nil {
		slog.Error("oauth exchange", slog.String("error", err.Error()))
		writeError(writer, http.StatusBadGateway, "oauth_failed", "Google sign-in failed, try again")
		return
	}
	user, err := server.params.Store.UpsertIdentity(request.Context(), "google", profile.Sub, profile.Email, profile.Name, profile.Picture)
	if err != nil {
		slog.Error("oauth upsert", slog.String("error", err.Error()))
		writeError(writer, http.StatusInternalServerError, "internal", "could not create your account")
		return
	}
	rawToken, tokenHash, err := auth.NewSessionToken()
	if err != nil {
		slog.Error("session token", slog.String("error", err.Error()))
		writeError(writer, http.StatusInternalServerError, "internal", "could not create your session")
		return
	}
	if err := server.params.Store.CreateSession(request.Context(), user.ID, tokenHash, time.Now().Add(server.params.SessionTTL)); err != nil {
		slog.Error("session create", slog.String("error", err.Error()))
		writeError(writer, http.StatusInternalServerError, "internal", "could not create your session")
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookieName, Value: rawToken, Path: "/",
		MaxAge:   int(server.params.SessionTTL.Seconds()),
		HttpOnly: true, Secure: server.params.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	// Clear the state cookie, its job is done.
	http.SetCookie(writer, &http.Cookie{Name: stateCookieName, Value: "", Path: "/v1/auth", MaxAge: -1})
	http.Redirect(writer, request, "/", http.StatusFound)
}

func (server *Server) handleLogout(writer http.ResponseWriter, request *http.Request) {
	if token := bearerOrCookieToken(request); token != "" {
		if err := server.params.Store.DeleteSession(request.Context(), auth.HashToken(token)); err != nil {
			slog.Error("logout", slog.String("error", err.Error()))
		}
	}
	http.SetCookie(writer, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleMe(writer http.ResponseWriter, request *http.Request) {
	user := currentUser(request)
	writeJSON(writer, http.StatusOK, map[string]any{
		"id": user.ID, "email": user.Email, "display_name": user.DisplayName, "avatar_url": user.AvatarURL,
	})
}
