package main

import (
	"context"
	"gw2lfgserver/clientinfo"
	"gw2lfgserver/database"
	"gw2lfgserver/kpme"
	pb "gw2lfgserver/pb"
	"gw2lfgserver/syncmap"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	pb.UnimplementedLfgServiceServer
	db                        *database.DB
	kpClient                  *kpme.Client
	groupsSubscribers         *syncmap.Map[string, chan *pb.GroupsUpdate]
	applicationsSubscribers   *syncmap.Map[string, *syncmap.Map[string, chan *pb.GroupApplicationUpdate]]
	myApplicationsSubscribers *syncmap.Map[string, chan *pb.GroupApplicationUpdate]
}

func NewServer(db *database.DB, kpClient *kpme.Client) *Server {
	return &Server{
		db:                        db,
		kpClient:                  kpClient,
		groupsSubscribers:         syncmap.New[string, chan *pb.GroupsUpdate](),
		applicationsSubscribers:   syncmap.New[string, *syncmap.Map[string, chan *pb.GroupApplicationUpdate]](),
		myApplicationsSubscribers: syncmap.New[string, chan *pb.GroupApplicationUpdate](),
	}
}

func (s *Server) SubscribeGroups(req *pb.SubscribeGroupsRequest, stream pb.LfgService_SubscribeGroupsServer) error {
	clientInfo := clientinfo.FromContext(stream.Context())
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
			return nil
		}
	}
}

// Group Management
func (s *Server) CreateGroup(ctx context.Context, req *pb.CreateGroupRequest) (*pb.CreateGroupResponse, error) {
	client := mustGetClient(ctx)

	if err := s.validateNewGroup(ctx, client.AccountName); err != nil {
		return nil, err
	}

	now := time.Now()
	group := &pb.Group{
		Id:               uuid.New().String(),
		CreatorId:        client.AccountName,
		Title:            req.Title,
		KillProofId:      req.KillProofId,
		KillProofMinimum: req.KillProofMinimum,
		CreatedAtSec:     now.Unix(),
		UpdatedAtSec:     now.Unix(),
	}

	savedGroup, err := s.db.SaveGroup(ctx, group)
	if err != nil {
		slog.ErrorContext(ctx, "s.db.SaveGroup", "err", err)
		return nil, status.Error(codes.Internal, "Failed to create group")
	}

	s.broadcastGroupUpdate(&pb.GroupsUpdate{
		Update: &pb.GroupsUpdate_NewGroup{NewGroup: savedGroup},
	})

	return &pb.CreateGroupResponse{Group: savedGroup}, nil
}

func (s *Server) UpdateGroup(ctx context.Context, req *pb.UpdateGroupRequest) (*pb.UpdateGroupResponse, error) {
	client := mustGetClient(ctx)
	group := req.GetGroup()

	if err := s.validateGroupOwnership(ctx, group.Id, client.AccountName); err != nil {
		return nil, err
	}

	now := time.Now()
	group.UpdatedAtSec = now.Unix()
	savedGroup, err := s.db.SaveGroup(ctx, group)
	if err != nil {
		slog.ErrorContext(ctx, "s.db.SaveGroup", "err", err)
		return nil, status.Error(codes.Internal, "Failed to update group")
	}

	s.broadcastGroupUpdate(&pb.GroupsUpdate{
		Update: &pb.GroupsUpdate_UpdatedGroup{UpdatedGroup: savedGroup},
	})

	return &pb.UpdateGroupResponse{Group: savedGroup}, nil
}

func (s *Server) validateNewGroup(ctx context.Context, accountID string) error {
	groups, err := s.db.ListGroups(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "s.db.ListGroups", "err", err)
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
		slog.ErrorContext(ctx, "s.db.GetGroup", "err", err)
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

func mustGetClient(ctx context.Context) *clientinfo.ClientInfo {
	client := clientinfo.FromContext(ctx)
	if client == nil {
		panic("authentication middleware failed to inject client info")
	}
	return client
}

func (s *Server) DeleteGroup(ctx context.Context, req *pb.DeleteGroupRequest) (*pb.DeleteGroupResponse, error) {
	clientInfo := clientinfo.FromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	group, err := s.db.GetGroup(ctx, req.GroupId)
	if err != nil {
		slog.ErrorContext(ctx, "s.db.GetGroup", "err", err)
		return nil, status.Errorf(codes.Internal, "Failed to get group: %v", err)
	}
	if group == nil {
		return nil, nil
	}
	if clientInfo.AccountName != group.CreatorId {
		return nil, status.Error(codes.PermissionDenied, "Not group creator")
	}

	// Delete from database
	if err := s.db.DeleteGroup(ctx, group.Id); err != nil {
		slog.ErrorContext(ctx, "s.db.DeleteGroup", "err", err)
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
	groups, err := s.db.ListGroups(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "s.db.ListGroups", "err", err)
		return nil, status.Error(codes.Internal, "Failed to list groups")
	}
	return &pb.ListGroupsResponse{
		Groups: groups,
	}, nil
}

// Application Management
func (s *Server) CreateGroupApplication(ctx context.Context, req *pb.CreateGroupApplicationRequest) (*pb.CreateGroupApplicationResponse, error) {
	client := mustGetClient(ctx)

	if err := s.validateApplication(ctx, req.GroupId, client.AccountName); err != nil {
		return nil, err
	}

	now := time.Now()
	application := &pb.GroupApplication{
		Id:           uuid.New().String(),
		AccountName:  client.AccountName,
		GroupId:      req.GroupId,
		CreatedAtSec: now.Unix(),
		UpdatedAtSec: now.Unix(),
	}

	savedApp, err := s.db.SaveApplication(ctx, application, req.GroupId)
	if err != nil {
		slog.ErrorContext(ctx, "s.db.SaveApplication", "err", err)
		return nil, status.Error(codes.Internal, "Failed to create application")
	}

	kp, err := s.kpClient.GetKillProof(savedApp.AccountName)
	if err != nil {
		slog.ErrorContext(ctx, "s.kpClient.GetKillProof", "err", err)
	} else if kp != nil {
		savedApp.KillProof = kpme.KillProofReponseToProto(kp)
	}

	s.broadcastApplicationUpdate(req.GroupId, savedApp.AccountName, &pb.GroupApplicationUpdate{
		Update: &pb.GroupApplicationUpdate_NewApplication{NewApplication: savedApp},
	})

	return &pb.CreateGroupApplicationResponse{Application: savedApp}, nil
}

func (s *Server) validateApplication(ctx context.Context, groupID, accountID string) error {
	group, err := s.db.GetGroup(ctx, groupID)
	if err != nil {
		slog.ErrorContext(ctx, "s.db.GetGroup", "err", err)
		return status.Error(codes.Internal, "Failed to validate application")
	}
	if group == nil {
		return status.Error(codes.NotFound, "Group not found")
	}
	if group.CreatorId == accountID {
		return status.Error(codes.PermissionDenied, "Cannot apply to own group")
	}

	applications, err := s.db.ListApplicationsForGroup(ctx, groupID)
	if err != nil {
		slog.ErrorContext(ctx, "s.db.ListApplications", "err", err)
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
	clientInfo := clientinfo.FromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	application, err := s.db.GetApplication(ctx, req.ApplicationId)
	if err != nil {
		slog.ErrorContext(ctx, "s.db.GetApplication", "err", err)
		return nil, status.Errorf(codes.Internal, "Failed to get application: %v", err)
	}
	if application == nil {
		return nil, status.Error(codes.NotFound, "Application not found")
	}
	if clientInfo.AccountName != application.AccountName {
		return nil, status.Error(codes.PermissionDenied, "Not application owner")
	}

	// Delete from database
	if err := s.db.DeleteApplication(ctx, req.ApplicationId); err != nil {
		slog.ErrorContext(ctx, "s.db.DeleteApplication", "err", err)
		return nil, status.Error(codes.Internal, "Failed to delete application")
	}

	s.broadcastApplicationUpdate(application.GroupId, application.AccountName, &pb.GroupApplicationUpdate{ // Update this line
		Update: &pb.GroupApplicationUpdate_RemovedApplicationId{
			RemovedApplicationId: application.Id,
		},
	})

	return &pb.DeleteGroupApplicationResponse{}, nil
}

func (s *Server) ListGroupApplications(ctx context.Context, req *pb.ListGroupApplicationsRequest) (*pb.ListGroupApplicationsResponse, error) {
	clientInfo := clientinfo.FromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	var applications []*pb.GroupApplication
	if req.GetAccountName() != "" {
		if req.GetAccountName() != clientInfo.AccountName {
			return nil, status.Error(codes.PermissionDenied, "Account ID mismatch")
		}

		apps, err := s.db.ListApplicationsForAccount(ctx, req.GetAccountName())
		if err != nil {
			slog.ErrorContext(ctx, "s.db.ListApplications", "err", err)
			return nil, status.Error(codes.Internal, "Failed to list applications")
		}
		applications = apps
	} else if req.GetGroupId() != "" {
		group, err := s.db.GetGroup(ctx, req.GetGroupId())
		if err != nil {
			slog.ErrorContext(ctx, "s.db.GetGroup", "err", err)
			return nil, status.Error(codes.NotFound, "Group not found")
		}
		if group == nil {
			return nil, status.Error(codes.NotFound, "Group not found")
		}

		if clientInfo.AccountName != group.CreatorId {
			return nil, status.Error(codes.PermissionDenied, "Not group creator")
		}

		apps, err := s.db.ListApplicationsForGroup(ctx, req.GetGroupId())
		if err != nil {
			slog.ErrorContext(ctx, "s.db.ListApplications", "err", err)
			return nil, status.Error(codes.Internal, "Failed to list applications")
		}
		applications = apps
	} else {
		return nil, status.Error(codes.InvalidArgument, "Either account name or group ID must be provided")
	}

	for _, app := range applications {
		kp, err := s.kpClient.GetKillProof(app.AccountName)
		if err != nil {
			slog.ErrorContext(ctx, "s.kpClient.GetKillProof", "err", err)
			continue
		}
		if kp == nil {
			continue
		}
		app.KillProof = kpme.KillProofReponseToProto(kp)
	}

	return &pb.ListGroupApplicationsResponse{
		Applications: applications,
	}, nil
}

func (s *Server) SubscribeGroupApplications(req *pb.SubscribeGroupApplicationsRequest, stream pb.LfgService_SubscribeGroupApplicationsServer) error {
	clientInfo := clientinfo.FromContext(stream.Context())
	if clientInfo == nil {
		return status.Error(codes.PermissionDenied, "Not authenticated")
	}
	applications := make(chan *pb.GroupApplicationUpdate, 100)
	s.myApplicationsSubscribers.Set(clientInfo.AccountName, applications)

	group, err := s.db.GetGroup(stream.Context(), req.GroupId)
	if err != nil {
		slog.ErrorContext(stream.Context(), "s.db.GetGroup", "err", err)
		return status.Error(codes.NotFound, "Group not found")
	}

	if group != nil {
		if clientInfo.AccountName != group.CreatorId {
			return status.Error(codes.PermissionDenied, "Not group creator")
		}

		s.applicationsSubscribers.Update(
			req.GroupId,
			func(subscribers *syncmap.Map[string, chan *pb.GroupApplicationUpdate], ok bool) (*syncmap.Map[string, chan *pb.GroupApplicationUpdate], bool) {
				if subscribers == nil {
					subscribers = syncmap.New[string, chan *pb.GroupApplicationUpdate]()
				}
				subscribers.Set(clientInfo.Token, applications)
				return subscribers, true
			})
	}

	// TODO: What if the group gets deleted?
	defer func() {
		subs, ok := s.applicationsSubscribers.Get(req.GroupId)
		if ok {
			subs.Delete(clientInfo.Token)
		}
		s.myApplicationsSubscribers.Delete(clientInfo.Token)
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

func (s *Server) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	clientInfo := clientinfo.FromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	result, err := s.db.TouchGroupsAndApplications(ctx, clientInfo.AccountName)
	if err != nil {
		slog.ErrorContext(ctx, "s.db.TouchGroupsAndApplications", "err", err)
		return nil, status.Error(codes.Internal, "Failed to update last seen time")
	}

	// Broadcast updates for touched groups
	for _, group := range result.Groups {
		s.broadcastGroupUpdate(&pb.GroupsUpdate{
			Update: &pb.GroupsUpdate_UpdatedGroup{
				UpdatedGroup: group,
			},
		})
	}

	// Broadcast updates for touched applications
	for _, app := range result.Applications {
		s.broadcastApplicationUpdate(app.GroupId, app.AccountName, &pb.GroupApplicationUpdate{
			Update: &pb.GroupApplicationUpdate_UpdatedApplication{
				UpdatedApplication: app,
			},
		})
	}

	return &pb.HeartbeatResponse{}, nil
}

func (s *Server) CleanUpExpiredDatabaseEntries(ctx context.Context, dbEntryTTL time.Duration, cleanupInterval time.Duration) {
	t := time.NewTicker(cleanupInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Delete applications before groups to avoid the foreign key constraint
			applications, err := s.db.DeleteApplicationsUpdatedBefore(ctx, time.Now().Add(-dbEntryTTL))
			if err != nil {
				slog.ErrorContext(ctx, "s.db.DeleteApplicationsUpdatedBefore", "err", err)
			}
			for _, app := range applications {
				s.broadcastApplicationUpdate(app.GroupId, app.AccountName, &pb.GroupApplicationUpdate{
					Update: &pb.GroupApplicationUpdate_RemovedApplicationId{
						RemovedApplicationId: app.Id,
					},
				})
			}

			groups, err := s.db.DeleteGroupsUpdatedBefore(ctx, time.Now().Add(-dbEntryTTL))
			if err != nil {
				slog.ErrorContext(ctx, "s.db.DeleteGroupsUpdatedBefore", "err", err)
			}
			for _, group := range groups {
				s.broadcastGroupUpdate(&pb.GroupsUpdate{
					Update: &pb.GroupsUpdate_RemovedGroupId{
						RemovedGroupId: group.Id,
					},
				})
			}
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

func (s *Server) broadcastApplicationUpdate(groupId, applicantAccountName string, update *pb.GroupApplicationUpdate) {
	if subscribers, ok := s.applicationsSubscribers.Get(groupId); ok {
		for _, ch := range subscribers.Snapshot() {
			select {
			case ch <- update:
			default:
				// Channel full, skip
			}
		}
	}
	if subscriber, ok := s.myApplicationsSubscribers.Get(applicantAccountName); ok {
		select {
		case subscriber <- update:
		default:
			// Channel full, skip
		}
	}
}
