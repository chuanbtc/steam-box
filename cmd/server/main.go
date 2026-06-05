package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"

	"steam-box/internal/api"
	"steam-box/internal/auth"
	"steam-box/internal/cdk"
	"steam-box/internal/config"
	"steam-box/internal/db"
	"steam-box/internal/manifest"
)

func main() {
	configPath := flag.String("config", "config.json", "path to configuration file")
	flag.Parse()

	// ── Load configuration ───────────────────────────────────────────
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// ── Ensure data directory exists ─────────────────────────────────
	dataDir := filepath.Dir(cfg.DBPath)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("failed to create data directory %s: %v", dataDir, err)
	}

	// ── Initialise database ──────────────────────────────────────────
	database, err := db.InitDB(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to initialise database: %v", err)
	}

	// ── Seed default admin user if none exist ────────────────────────
	var userCount int64
	database.Model(&db.User{}).Count(&userCount)
	if userCount == 0 {
		salt := auth.GenerateSalt()
		hash := auth.HashPassword(cfg.AdminPass, salt)
		admin := db.User{
			ID:           uuid.New().String(),
			Username:     cfg.AdminUser,
			PasswordHash: hash,
			Salt:         salt,
			Role:         "superadmin",
			Enabled:      true,
		}
		if err := database.Create(&admin).Error; err != nil {
			log.Fatalf("failed to seed admin user: %v", err)
		}
		log.Printf("seeded default admin user: %s", cfg.AdminUser)
	}

	// ── Load ManifestHub depot keys (background) ────────────────────
	go func() {
		log.Println("loading ManifestHub depot keys...")
		if err := manifest.LoadDepotKeys(); err != nil {
			log.Printf("WARNING: depot keys load failed: %v (game lookup will still work, but lua won't have depot keys)", err)
		} else {
			log.Printf("loaded %d depot keys from ManifestHub", manifest.DepotKeysCount())
		}
	}()

	// ── Create services ──────────────────────────────────────────────
	cdkSvc := cdk.NewService(database)
	server := api.NewServer(database, cdkSvc, cfg)

	// ── Startup banner ───────────────────────────────────────────────
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	fmt.Println("=========================================")
	fmt.Println("  Steam Box (Go + OpenSteamTool)")
	fmt.Println("=========================================")
	fmt.Printf("  Listening on http://%s\n", addr)
	fmt.Printf("  Admin: http://%s/admin\n", addr)
	fmt.Printf("  Hook:  irm http://%s/hook | iex\n", addr)
	fmt.Println("  Default login: admin / admin123")
	fmt.Println("=========================================")

	// ── Graceful shutdown ────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := server.Run(); err != nil {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down gracefully...")
}
