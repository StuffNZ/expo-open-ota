package main

import (
	"context"
	"expo-open-ota/config"
	"expo-open-ota/internal/metrics"
	"expo-open-ota/internal/migration"
	"expo-open-ota/internal/observability"
	infrastructure "expo-open-ota/internal/router"
	"log"
	"net/http"

	"github.com/gorilla/handlers"

	_ "expo-open-ota/internal/migrations"
)

func init() {
	config.LoadConfig()
	metrics.InitMetrics()
}

func main() {
	otelShutdown, err := observability.Setup(context.Background())
	if err != nil {
		log.Fatalf("Failed to configure OpenTelemetry: %v", err)
	}
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			log.Printf("OpenTelemetry shutdown error: %v", err)
		}
	}()

	migration.RunMigrationsWithLock()

	container, cleanup := infrastructure.InitDependencies(context.Background())
	defer cleanup()
	router := infrastructure.NewRouter(container)
	log.Println("Server is running on port " + config.GetPort())
	corsOptions := handlers.CORS(
		handlers.AllowedHeaders([]string{"Authorization", "Content-Type"}),
		handlers.AllowedMethods([]string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}),
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowCredentials(),
	)
	err = http.ListenAndServe("0.0.0.0:"+config.GetPort(), corsOptions(router))
	if err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
