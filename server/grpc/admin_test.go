package grpc

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

func TestGenerateToken(t *testing.T) {
	svc := &AdminService{
		baseInterval: 30 * time.Minute,
	}

	resp, err := svc.GenerateToken(context.Background(), &pb.GenerateTokenRequest{})
	if err != nil {
		t.Fatalf("GenerateToken() error: %v", err)
	}

	if resp.Token == "" {
		t.Fatal("GenerateToken() returned empty token")
	}

	// Token should be 64 hex chars (32 bytes)
	if len(resp.Token) != 64 {
		t.Errorf("GenerateToken() token length = %d, want 64", len(resp.Token))
	}

	// Token should be valid hex
	if _, err := hex.DecodeString(resp.Token); err != nil {
		t.Errorf("GenerateToken() token is not valid hex: %v", err)
	}

	// Two tokens should differ (randomness check)
	resp2, err := svc.GenerateToken(context.Background(), &pb.GenerateTokenRequest{})
	if err != nil {
		t.Fatalf("GenerateToken() second call error: %v", err)
	}
	if resp.Token == resp2.Token {
		t.Error("GenerateToken() returned same token twice -- not random")
	}
}

func TestAddApprovedPatternValidation(t *testing.T) {
	svc := &AdminService{
		baseInterval: 30 * time.Minute,
	}

	tests := []struct {
		name    string
		req     *pb.AddApprovedPatternRequest
		wantErr bool
	}{
		{
			name:    "empty pattern",
			req:     &pb.AddApprovedPatternRequest{Pattern: "", Token: "abc123"},
			wantErr: true,
		},
		{
			name:    "empty token",
			req:     &pb.AddApprovedPatternRequest{Pattern: "*.web.example.com", Token: ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.AddApprovedPattern(context.Background(), tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("AddApprovedPattern() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestForcePushValidation(t *testing.T) {
	svc := &AdminService{
		baseInterval: 30 * time.Minute,
	}

	// Invalid UUID should return error
	_, err := svc.ForcePush(context.Background(), &pb.ForcePushRequest{ClientId: "not-a-uuid"})
	if err == nil {
		t.Error("ForcePush() with invalid UUID should return error")
	}
}
