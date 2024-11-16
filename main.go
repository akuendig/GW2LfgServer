package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/improbable-eng/grpc-web/go/grpcweb"

	pb "gw2lfgserver/pb"
	"gw2lfgserver/syncmap"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type keyResolver struct{}

type account struct {
	Id           string    `json:"id"`
	Age          int       `json:"age"`
	Name         string    `json:"name"`
	World        int       `json:"world"`
	Guilds       []string  `json:"guilds"`
	GuildLeader  *[]string `json:"guild_leader,omitempty"` // Requires additional guilds scope
	Created      string    `json:"created"`
	Access       []string  `json:"access"`
	Commander    bool      `json:"commander"`
	FractalLevel *int      `json:"fractal_level,omitempty"` // Requires additional progression scope
	DailyAP      *int      `json:"daily_ap,omitempty"`      // Requires additional progression scope
	MonthlyAP    *int      `json:"monthly_ap,omitempty"`    // Requires additional progression scope
	WvwRank      *int      `json:"wvw_rank,omitempty"`      // Requires additional progression scope
	Wvw          *struct {
		TeamID int  `json:"team_id"`
		Rank   *int `json:"rank,omitempty"` // Requires additional progression scope
	} `json:"wvw,omitempty"` // Only present with specific schema versions
	LastModified      *string `json:"last_modified,omitempty"`       // Only present with specific schema versions
	BuildStorageSlots *int    `json:"build_storage_slots,omitempty"` // Requires additional builds scope
}

func (k *keyResolver) Resolve(ctx context.Context, key string) (string, error) {
	accountRes, err := http.DefaultClient.Get("https://api.guildwars2.com/v2/account?access_token=" + key)
	if err != nil {
		return "", status.Error(codes.Internal, fmt.Errorf("failed to resolve key: %w", err).Error())
	}
	defer accountRes.Body.Close()

	var account account
	if err = json.NewDecoder(accountRes.Body).Decode(&account); err != nil {
		return "", status.Error(codes.Internal, fmt.Errorf("failed to decode account: %w", err).Error())
	}
	return account.Name, nil
}

type server struct {
	pb.UnimplementedLfgServiceServer

	groups            *syncmap.Map[string, *pb.Group]
	groupsSubscribers *syncmap.Map[string, chan *pb.GroupsUpdate]

	applications            *syncmap.Map[string, []*pb.JoinGroupRequest]
	applicationsSubscribers *syncmap.Map[string, *syncmap.Map[string, chan *pb.JoinGroupRequest]]

	resolver keyResolver
}

func (s *server) SubscribeGroups(req *pb.SubscribeGroupsRequest, stream pb.LfgService_SubscribeGroupsServer) error {
	log.Println("SubscribeGroups")
	updates := make(chan *pb.GroupsUpdate, 100)

	// Register subscriber
	// TODO: What if the same user subscribes twice?
	s.groupsSubscribers.Set(req.ClientKey, updates)

	defer func() {
		s.groupsSubscribers.Delete(req.ClientKey)
		close(updates)
	}()

	// Send updates
	for {
		select {
		case update := <-updates:
			if err := stream.Send(update); err != nil {
				return err
			}
		case <-stream.Context().Done():
			log.Println("SubscribeGroups done")
			return nil
		}
	}
}

func (s *server) CreateGroup(ctx context.Context, req *pb.CreateGroupRequest) (*pb.CreateGroupResponse, error) {
	log.Println("CreateGroup")
	accountId, err := s.resolver.Resolve(ctx, req.ClientKey)
	if err != nil {
		return nil, err
	}
	if accountId == "" {
		return nil, status.Error(codes.PermissionDenied, "Invalid key")
	}

	for _, group := range s.groups.Snapshot() {
		if group.CreatorId == accountId {
			return nil, status.Error(codes.PermissionDenied, "Already owns a group")
		}
	}

	// Create group
	group := &pb.Group{
		Id:               uuid.New().String(),
		CreatorId:        accountId,
		Title:            req.Title,
		KillProofId:      req.KillProofId,
		KillProofMinimum: req.KillProofMinimum,
		CreatedAtSec:     time.Now().Unix(),
	}

	s.groups.Set(group.Id, group)

	// Save to database
	if err := s.saveGroup(ctx, group); err != nil {
		return nil, status.Error(codes.Internal, "Failed to create group")
	}

	// Broadcast to all subscribers
	s.broadcast(&pb.GroupsUpdate{
		Update: &pb.GroupsUpdate_NewGroup{
			NewGroup: group,
		},
	})

	return &pb.CreateGroupResponse{
		Group: group,
	}, nil
}

func (s *server) UpdateGroup(ctx context.Context, req *pb.UpdateGroupRequest) (*pb.UpdateGroupResponse, error) {
	log.Println("UpdateGroup")
	group, exists := s.groups.Get(req.Group.Id)
	if group == nil || !exists {
		return nil, status.Error(codes.NotFound, "Group not found")
	}

	accountId, err := s.resolver.Resolve(ctx, req.ClientKey)
	if err != nil {
		return nil, err
	}
	if accountId != group.CreatorId {
		return nil, status.Error(codes.PermissionDenied, "Not group creator")
	}

	group.Title = req.Group.Title
	group.KillProofId = req.Group.KillProofId
	group.KillProofMinimum = req.Group.KillProofMinimum

	s.groups.Set(group.Id, group)

	// Save to database
	if err := s.saveGroup(ctx, group); err != nil {
		return nil, status.Error(codes.Internal, "Failed to update group")
	}

	// Broadcast to all subscribers
	s.broadcast(&pb.GroupsUpdate{
		Update: &pb.GroupsUpdate_UpdatedGroup{
			UpdatedGroup: group,
		},
	})

	return &pb.UpdateGroupResponse{
		Group: group,
	}, nil
}

func (s *server) DeleteGroup(ctx context.Context, req *pb.DeleteGroupRequest) (*pb.DeleteGroupResponse, error) {
	log.Println("DeleteGroup")
	group, exists := s.groups.Get(req.GroupId)
	if group == nil || !exists {
		return &pb.DeleteGroupResponse{}, nil
	}

	accountId, err := s.resolver.Resolve(ctx, req.ClientKey)
	if err != nil {
		return nil, err
	}
	if accountId != group.CreatorId {
		return nil, status.Error(codes.PermissionDenied, "Not group creator")
	}

	s.groups.Delete(group.Id)

	// Delete from database
	if err := s.deleteGroup(ctx, group.Id); err != nil {
		return nil, status.Error(codes.Internal, "Failed to delete group")
	}

	// Broadcast to all subscribers
	s.broadcast(&pb.GroupsUpdate{
		Update: &pb.GroupsUpdate_RemovedGroupId{
			RemovedGroupId: group.Id,
		},
	})

	return &pb.DeleteGroupResponse{}, nil
}

func (s *server) ListGroups(ctx context.Context, req *pb.ListGroupsRequest) (*pb.ListGroupsResponse, error) {
	log.Println("ListGroups")
	groups := s.groups.Snapshot()
	return &pb.ListGroupsResponse{
		Groups: groups,
	}, nil
}

func (s *server) SubscribeToApplications(req *pb.SubscribeToApplicationsRequest, stream pb.LfgService_SubscribeToApplicationsServer) error {
	log.Println("SubscribeToApplications")
	group, exists := s.groups.Get(req.GroupId)
	if group == nil || !exists {
		return status.Error(codes.NotFound, "Group not found")
	}

	accountId, err := s.resolver.Resolve(stream.Context(), req.ClientKey)
	if err != nil {
		return err
	}
	if accountId != group.CreatorId {
		return status.Error(codes.PermissionDenied, "Not group creator")
	}

	applications := make(chan *pb.JoinGroupRequest, 100)
	s.applicationsSubscribers.Update(
		req.GroupId,
		func(subscribers *syncmap.Map[string, chan *pb.JoinGroupRequest], ok bool) (*syncmap.Map[string, chan *pb.JoinGroupRequest], bool) {
			if subscribers == nil {
				subscribers = syncmap.New[string, chan *pb.JoinGroupRequest]()
			}
			subscribers.Set(req.ClientKey, applications)
			return subscribers, true
		})

	defer func() {
		subs, ok := s.applicationsSubscribers.Get(req.GroupId)
		if ok {
			subs.Delete(req.ClientKey)
		}
		close(applications)
	}()

	for {
		select {
		case app := <-applications:
			if err := stream.Send(app); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

func (s *server) broadcast(update *pb.GroupsUpdate) {
	for _, ch := range s.groupsSubscribers.Snapshot() {
		select {
		case ch <- update:
		default:
			// Channel full, skip
		}
	}
}

func (s *server) saveGroup(ctx context.Context, group *pb.Group) error {
	// Save to database
	return nil
}

func (s *server) deleteGroup(ctx context.Context, groupId string) error {
	// Save to database
	return nil
}

func main() {
	s := &server{
		groups:                  syncmap.New[string, *pb.Group](),
		groupsSubscribers:       syncmap.New[string, chan *pb.GroupsUpdate](),
		applications:            syncmap.New[string, []*pb.JoinGroupRequest](),
		applicationsSubscribers: syncmap.New[string, *syncmap.Map[string, chan *pb.JoinGroupRequest]](),
	}

	grpcServer := grpc.NewServer()
	pb.RegisterLfgServiceServer(grpcServer, s)
	reflection.Register(grpcServer)
	wrappedGrpc := grpcweb.WrapServer(grpcServer,
		grpcweb.WithOriginFunc(func(origin string) bool {
			// Allow all origins for development
			return true
		}),
		grpcweb.WithAllowedRequestHeaders([]string{"*"}),
	)

	// Start HTTP server
	server := &http.Server{
		Addr:    "127.0.0.1:5001",
		Handler: wrappedGrpc,
	}

	log.Printf("Starting server on %s", server.Addr)
	log.Printf("Listing the following resources: %v", grpcweb.ListGRPCResources(grpcServer))
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
