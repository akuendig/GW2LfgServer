package main

import (
	"context"
	"gw2lfgserver/database"
	pb "gw2lfgserver/pb"
	"gw2lfgserver/syncmap"
	"log"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	pb.UnimplementedLfgServiceServer
	db                      *database.DB
	groupsSubscribers       *syncmap.Map[string, chan *pb.GroupsUpdate]
	applicationsSubscribers *syncmap.Map[string, *syncmap.Map[string, chan *pb.GroupApplicationUpdate]]
}

func NewServer(db *database.DB) *Server {
	return &Server{
		db:                      db,
		groupsSubscribers:       syncmap.New[string, chan *pb.GroupsUpdate](),
		applicationsSubscribers: syncmap.New[string, *syncmap.Map[string, chan *pb.GroupApplicationUpdate]](),
	}
}

func (s *Server) SubscribeGroups(req *pb.SubscribeGroupsRequest, stream pb.LfgService_SubscribeGroupsServer) error {
	log.Println("SubscribeGroups")
	clientInfo := clientInfoFromContext(stream.Context())
	if clientInfo == nil {
		return status.Error(codes.PermissionDenied, "Not authenticated")
	}

	updates := make(chan *pb.GroupsUpdate, 100)

	// Register subscriber
	// TODO: What if the same user subscribes twice?
	s.groupsSubscribers.Set(clientInfo.Token, updates)

	defer func() {
		s.groupsSubscribers.Delete(clientInfo.Token)
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

// Group Management
func (s *Server) CreateGroup(ctx context.Context, req *pb.CreateGroupRequest) (*pb.CreateGroupResponse, error) {
	client := mustGetClient(ctx)

	if err := s.validateNewGroup(ctx, client.AccountID); err != nil {
		return nil, err
	}

	group := &pb.Group{
		Id:               uuid.New().String(),
		CreatorId:        client.AccountID,
		Title:            req.Title,
		KillProofId:      req.KillProofId,
		KillProofMinimum: req.KillProofMinimum,
		CreatedAtSec:     time.Now().Unix(),
	}

	if err := s.db.SaveGroup(ctx, group); err != nil {
		return nil, status.Error(codes.Internal, "Failed to create group")
	}

	s.broadcastGroupUpdate(&pb.GroupsUpdate{
		Update: &pb.GroupsUpdate_NewGroup{NewGroup: group},
	})

	return &pb.CreateGroupResponse{Group: group}, nil
}

func (s *Server) UpdateGroup(ctx context.Context, req *pb.UpdateGroupRequest) (*pb.UpdateGroupResponse, error) {
	client := mustGetClient(ctx)
	group := req.GetGroup()

	if err := s.validateGroupOwnership(ctx, group.Id, client.AccountID); err != nil {
		return nil, err
	}

	if err := s.db.SaveGroup(ctx, group); err != nil {
		return nil, status.Error(codes.Internal, "Failed to update group")
	}

	s.broadcastGroupUpdate(&pb.GroupsUpdate{
		Update: &pb.GroupsUpdate_UpdatedGroup{UpdatedGroup: group},
	})

	return &pb.UpdateGroupResponse{Group: group}, nil
}

func (s *Server) validateNewGroup(ctx context.Context, accountID string) error {
	groups, err := s.db.ListGroups(ctx)
	if err != nil {
		return status.Error(codes.Internal, "Failed to validate group creation")
	}

	for _, group := range groups {
		if group.CreatorId == accountID {
			return status.Error(codes.PermissionDenied, "User already owns a group")
		}
	}
	return nil
}

func (s *Server) validateGroupOwnership(ctx context.Context, groupID, accountID string) error {
	group, err := s.db.GetGroup(ctx, groupID)
	if err != nil {
		return status.Error(codes.Internal, "Failed to validate group ownership")
	}
	if group == nil {
		return status.Error(codes.NotFound, "Group not found")
	}
	if group.CreatorId != accountID {
		return status.Error(codes.PermissionDenied, "Not group owner")
	}
	return nil
}

func mustGetClient(ctx context.Context) *clientInfo {
	client := clientInfoFromContext(ctx)
	if client == nil {
		panic("authentication middleware failed to inject client info")
	}
	return client
}

func (s *Server) DeleteGroup(ctx context.Context, req *pb.DeleteGroupRequest) (*pb.DeleteGroupResponse, error) {
	log.Println("DeleteGroup")
	clientInfo := clientInfoFromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	group, err := s.db.GetGroup(ctx, req.GroupId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get group: %v", err)
	}
	if group == nil {
		return nil, nil
	}
	if clientInfo.AccountID != group.CreatorId {
		return nil, status.Error(codes.PermissionDenied, "Not group creator")
	}

	// Delete from database
	if err := s.db.DeleteGroup(ctx, group.Id); err != nil {
		return nil, status.Error(codes.Internal, "Failed to delete group")
	}

	// Broadcast to all subscribers
	s.broadcastGroupUpdate(&pb.GroupsUpdate{
		Update: &pb.GroupsUpdate_RemovedGroupId{
			RemovedGroupId: group.Id,
		},
	})

	return &pb.DeleteGroupResponse{}, nil
}

func (s *Server) ListGroups(ctx context.Context, req *pb.ListGroupsRequest) (*pb.ListGroupsResponse, error) {
	log.Println("ListGroups")
	groups, err := s.db.ListGroups(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "Failed to list groups")
	}
	return &pb.ListGroupsResponse{
		Groups: groups,
	}, nil
}

// Application Management
func (s *Server) CreateGroupApplication(ctx context.Context, req *pb.CreateGroupApplicationRequest) (*pb.CreateGroupApplicationResponse, error) {
	client := mustGetClient(ctx)

	if err := s.validateApplication(ctx, req.GroupId, client.AccountID); err != nil {
		return nil, err
	}

	application := &pb.GroupApplication{
		Id:          uuid.New().String(),
		AccountName: client.AccountID,
		GroupId:     req.GroupId,
	}

	if err := s.db.SaveApplication(ctx, application, req.GroupId); err != nil {
		return nil, status.Error(codes.Internal, "Failed to create application")
	}

	s.broadcastApplicationUpdate(req.GroupId, &pb.GroupApplicationUpdate{
		Update: &pb.GroupApplicationUpdate_NewApplication{NewApplication: application},
	})

	return &pb.CreateGroupApplicationResponse{Application: application}, nil
}

func (s *Server) validateApplication(ctx context.Context, groupID, accountID string) error {
	group, err := s.db.GetGroup(ctx, groupID)
	if err != nil {
		return status.Error(codes.Internal, "Failed to validate application")
	}
	if group == nil {
		return status.Error(codes.NotFound, "Group not found")
	}
	if group.CreatorId == accountID {
		return status.Error(codes.PermissionDenied, "Cannot apply to own group")
	}

	applications, err := s.db.ListApplications(ctx, groupID)
	if err != nil {
		return status.Error(codes.Internal, "Failed to check existing applications")
	}

	for _, app := range applications {
		if app.AccountName == accountID {
			return status.Error(codes.AlreadyExists, "Already applied to this group")
		}
	}
	return nil
}

func (s *Server) DeleteGroupApplication(ctx context.Context, req *pb.DeleteGroupApplicationRequest) (*pb.DeleteGroupApplicationResponse, error) {
	log.Println("DeleteGroupApplication")
	clientInfo := clientInfoFromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	application, err := s.db.GetApplication(ctx, req.ApplicationId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to get application: %v", err)
	}
	if application == nil {
		return nil, status.Error(codes.NotFound, "Application not found")
	}
	if clientInfo.AccountID != application.AccountName {
		return nil, status.Error(codes.PermissionDenied, "Not application owner")
	}

	// Delete from database
	if err := s.db.DeleteApplication(ctx, req.ApplicationId); err != nil {
		return nil, status.Error(codes.Internal, "Failed to delete application")
	}

	s.broadcastApplicationUpdate(application.GroupId, &pb.GroupApplicationUpdate{ // Update this line
		Update: &pb.GroupApplicationUpdate_RemovedApplicationId{
			RemovedApplicationId: application.Id,
		},
	})

	return &pb.DeleteGroupApplicationResponse{}, nil
}

func (s *Server) ListGroupApplications(ctx context.Context, req *pb.ListGroupApplicationsRequest) (*pb.ListGroupApplicationsResponse, error) {
	log.Println("ListGroupApplications")
	clientInfo := clientInfoFromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	group, err := s.db.GetGroup(ctx, req.GroupId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "Group not found")
	}

	if clientInfo.AccountID != group.CreatorId {
		return nil, status.Error(codes.PermissionDenied, "Not group creator")
	}

	applications, err := s.db.ListApplications(ctx, req.GroupId)
	if err != nil {
		return nil, status.Error(codes.Internal, "Failed to list applications")
	}

	return &pb.ListGroupApplicationsResponse{
		Applications: applications,
	}, nil
}

func (s *Server) SubscribeGroupApplications(req *pb.SubscribeGroupApplicationsRequest, stream pb.LfgService_SubscribeGroupApplicationsServer) error {
	log.Println("SubscribeGroupApplications")
	clientInfo := clientInfoFromContext(stream.Context())
	if clientInfo == nil {
		return status.Error(codes.PermissionDenied, "Not authenticated")
	}

	group, err := s.db.GetGroup(stream.Context(), req.GroupId)
	if err != nil {
		return status.Error(codes.NotFound, "Group not found")
	}

	if clientInfo.AccountID != group.CreatorId {
		return status.Error(codes.PermissionDenied, "Not group creator")
	}

	applications := make(chan *pb.GroupApplicationUpdate, 100)
	s.applicationsSubscribers.Update(
		req.GroupId,
		func(subscribers *syncmap.Map[string, chan *pb.GroupApplicationUpdate], ok bool) (*syncmap.Map[string, chan *pb.GroupApplicationUpdate], bool) {
			if subscribers == nil {
				subscribers = syncmap.New[string, chan *pb.GroupApplicationUpdate]()
			}
			subscribers.Set(clientInfo.Token, applications)
			return subscribers, true
		})

	// TODO: What if the group gets deleted?
	defer func() {
		subs, ok := s.applicationsSubscribers.Get(req.GroupId)
		if ok {
			subs.Delete(clientInfo.Token)
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

func (s *Server) broadcastGroupUpdate(update *pb.GroupsUpdate) {
	for _, ch := range s.groupsSubscribers.Snapshot() {
		select {
		case ch <- update:
		default:
			// Channel full, skip
		}
	}
}

func (s *Server) broadcastApplicationUpdate(groupId string, update *pb.GroupApplicationUpdate) {
	subscribers, ok := s.applicationsSubscribers.Get(groupId)
	if !ok {
		return
	}
	for _, ch := range subscribers.Snapshot() {
		select {
		case ch <- update:
		default:
			// Channel full, skip
		}
	}
}
