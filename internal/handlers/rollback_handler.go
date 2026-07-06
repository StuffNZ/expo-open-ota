package handlers

import (
	"encoding/json"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/services"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type RollbackHandler struct {
	authService       *services.AuthService
	deploymentService *services.DeploymentService
}

func NewRollbackHandler(authService *services.AuthService, deploymentService *services.DeploymentService) *RollbackHandler {
	return &RollbackHandler{
		authService:       authService,
		deploymentService: deploymentService,
	}
}

func (h *RollbackHandler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	requestID := uuid.New().String()
	vars := mux.Vars(r)
	appId := vars["APP_ID"]
	branchName := vars["BRANCH"]
	platform := r.URL.Query().Get("platform")
	if platform == "" || (platform != "ios" && platform != "android") {
		log.Printf("[RequestID: %s] Invalid platform: %s", requestID, platform)
		http.Error(w, "Invalid platform", http.StatusBadRequest)
		return
	}
	if branchName == "" {
		log.Printf("[RequestID: %s] No branch provided", requestID)
		http.Error(w, "No branch provided", http.StatusBadRequest)
		return
	}
	auth := helpers.GetAuth(r)
	err := h.authService.ValidateAuth(r.Context(), appId, auth)
	if err != nil {
		log.Printf("[RequestID: %s] Error validating auth: %v", requestID, err)
		http.Error(w, "Error validating auth", http.StatusUnauthorized)
		return
	}
	runtimeVersion := r.URL.Query().Get("runtimeVersion")
	if runtimeVersion == "" {
		log.Printf("[RequestID: %s] No runtime version provided", requestID)
		http.Error(w, "No runtime version provided", http.StatusBadRequest)
		return
	}
	commitHash := r.URL.Query().Get("commitHash")
	// Route through RollbackRelease (not CreateRollback directly): it owns
	// the branch/runtime-version upsert, so rollbacks behave consistently in
	// both stateless and DB mode. Fixing the call also un-deadens the
	// orchestrating path, which previously had no callers.
	rollback, err := h.deploymentService.RollbackRelease(r.Context(), services.RollbackParams{
		AppID:          appId,
		BranchName:     branchName,
		Platform:       platform,
		RuntimeVersion: runtimeVersion,
		CommitHash:     commitHash,
		RequestID:      requestID,
	})
	if err != nil {
		log.Printf("[RequestID: %s] Error creating rollback: %v", requestID, err)
		http.Error(w, "Error creating rollback", http.StatusInternalServerError)
		return
	}
	log.Printf("[RequestID: %s] Rollback created: %s", requestID, rollback.UpdateId)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(rollback)
}
