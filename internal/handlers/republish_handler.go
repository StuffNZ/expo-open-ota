package handlers

import (
	"encoding/json"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/services"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type RepublishHandler struct {
	authService       *services.AuthService
	deploymentService *services.DeploymentService
}

func NewRepublishHandler(authService *services.AuthService, deploymentService *services.DeploymentService) *RepublishHandler {
	return &RepublishHandler{
		authService:       authService,
		deploymentService: deploymentService,
	}
}

func (h *RepublishHandler) HandleRepublish(w http.ResponseWriter, r *http.Request) {
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
	updateId := r.URL.Query().Get("updateId")
	if updateId == "" {
		log.Printf("[RequestID: %s] No updateId provided", requestID)
		http.Error(w, "No updateId provided", http.StatusBadRequest)
		return
	}
	// Route through RepublishRelease, NOT RepublishUpdate directly: the
	// release path owns the validations (source update exists, is a normal
	// valid update, platform matches). Calling the low-level create bypasses
	// all of them.
	newUpdate, err := h.deploymentService.RepublishRelease(r.Context(), services.RepublishParams{
		AppID:          appId,
		BranchName:     branchName,
		Platform:       platform,
		RuntimeVersion: runtimeVersion,
		CommitHash:     commitHash,
		UpdateID:       updateId,
		RequestID:      requestID,
	})
	if err != nil {
		log.Printf("[RequestID: %s] Error republishing update: %v", requestID, err)
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(newUpdate)
}
