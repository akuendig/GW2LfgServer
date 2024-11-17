package main

import (
	"context"
	pb "gw2lfgserver/pb"
	"gw2lfgserver/syncmap"
	"log"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type server struct {
	pb.UnimplementedLfgServiceServer

	groups            *syncmap.Map[string, *pb.Group]
	groupsSubscribers *syncmap.Map[string, chan *pb.GroupsUpdate]

	applications            *syncmap.Map[string, []*pb.GroupApplication]
	applicationsSubscribers *syncmap.Map[string, *syncmap.Map[string, chan *pb.GroupApplicationUpdate]]
}

func (s *server) SubscribeGroups(req *pb.SubscribeGroupsRequest, stream pb.LfgService_SubscribeGroupsServer) error {
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

func (s *server) CreateGroup(ctx context.Context, req *pb.CreateGroupRequest) (*pb.CreateGroupResponse, error) {
	log.Println("CreateGroup")
	clientInfo := clientInfoFromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	for _, group := range s.groups.Snapshot() {
		if group.CreatorId == clientInfo.AccountID {
			return nil, status.Error(codes.PermissionDenied, "Already owns a group")
		}
	}

	// Create group
	group := &pb.Group{
		Id:               uuid.New().String(),
		CreatorId:        clientInfo.AccountID,
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
	clientInfo := clientInfoFromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	group, exists := s.groups.Get(req.Group.Id)
	if group == nil || !exists {
		return nil, status.Error(codes.NotFound, "Group not found")
	}

	if clientInfo.AccountID != group.CreatorId {
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
	clientInfo := clientInfoFromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	group, exists := s.groups.Get(req.GroupId)
	if group == nil || !exists {
		return &pb.DeleteGroupResponse{}, nil
	}

	if clientInfo.AccountID != group.CreatorId {
		return nil, status.Error(codes.PermissionDenied, "Not group creator")
	}

	s.groups.Delete(group.Id)
	s.applications.Delete(group.Id)
	subscribers, ok := s.applicationsSubscribers.Delete(group.Id)
	if ok {
		for _, ch := range subscribers.Snapshot() {
			close(ch)
		}
	}

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

func (s *server) CreateGroupApplication(ctx context.Context, req *pb.CreateGroupApplicationRequest) (*pb.CreateGroupApplicationResponse, error) {
	log.Println("CreateGroupApplication")
	clientInfo := clientInfoFromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	group, exists := s.groups.Get(req.GroupId)
	if group == nil || !exists {
		return nil, status.Error(codes.NotFound, "Group not found")
	}

	if clientInfo.AccountID == group.CreatorId {
		// return nil, status.Error(codes.PermissionDenied, "Cannot join own group")
	}

	application := &pb.GroupApplication{
		AccountName: clientInfo.AccountID,
	}
	s.applications.Update(group.Id, func(applications []*pb.GroupApplication, ok bool) ([]*pb.GroupApplication, bool) {
		if !ok {
			applications = []*pb.GroupApplication{}
		}
		// Check if already applied
		for _, app := range applications {
			if app.AccountName == clientInfo.AccountID {
				return applications, true
			}
		}
		applications = append(applications, application)
		return applications, true
	})
	s.broadcastApplication(group.Id, &pb.GroupApplicationUpdate{
		Update: &pb.GroupApplicationUpdate_NewApplication{
			NewApplication: application,
		},
	})

	return &pb.CreateGroupApplicationResponse{
		Application: application,
	}, nil
}

func (s *server) DeleteGroupApplication(ctx context.Context, req *pb.DeleteGroupApplicationRequest) (*pb.DeleteGroupApplicationResponse, error) {
	log.Println("DeleteGroupApplication")
	clientInfo := clientInfoFromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	group, exists := s.groups.Get(req.GroupId)
	if group == nil || !exists {
		return nil, status.Error(codes.NotFound, "Group not found")
	}

	applications, _ := s.applications.Get(req.GroupId)
	var application *pb.GroupApplication
	for _, app := range applications {
		if app.Id == req.ApplicationId {
			application = app
			break
		}
	}

	if application == nil {
		return nil, status.Error(codes.NotFound, "Application not found")
	}

	if clientInfo.AccountID != application.AccountName {
		return nil, status.Error(codes.PermissionDenied, "Not application owner")
	}

	s.applications.Update(group.Id, func(applications []*pb.GroupApplication, ok bool) ([]*pb.GroupApplication, bool) {
		if !ok {
			return applications, true
		}
		for i, app := range applications {
			if app.Id == req.ApplicationId {
				applications = append(applications[:i], applications[i+1:]...)
				break
			}
		}
		return applications, true
	})
	s.broadcastApplication(group.Id, &pb.GroupApplicationUpdate{
		Update: &pb.GroupApplicationUpdate_RemovedApplicationId{
			RemovedApplicationId: application.Id,
		},
	})

	return &pb.DeleteGroupApplicationResponse{}, nil
}

func (s *server) ListGroupApplications(ctx context.Context, req *pb.ListGroupApplicationsRequest) (*pb.ListGroupApplicationsResponse, error) {
	log.Println("ListGroupApplications")
	clientInfo := clientInfoFromContext(ctx)
	if clientInfo == nil {
		return nil, status.Error(codes.PermissionDenied, "Not authenticated")
	}

	group, exists := s.groups.Get(req.GroupId)
	if group == nil || !exists {
		return nil, status.Error(codes.NotFound, "Group not found")
	}

	if clientInfo.AccountID != group.CreatorId {
		return nil, status.Error(codes.PermissionDenied, "Not group creator")
	}

	applications, exists := s.applications.Get(req.GroupId)
	if !exists {
		return &pb.ListGroupApplicationsResponse{}, nil
	}

	return &pb.ListGroupApplicationsResponse{
		Applications: applications,
	}, nil
}

func (s *server) SubscribeGroupApplications(req *pb.SubscribeGroupApplicationsRequest, stream pb.LfgService_SubscribeGroupApplicationsServer) error {
	log.Println("SubscribeGroupApplications")
	clientInfo := clientInfoFromContext(stream.Context())
	if clientInfo == nil {
		return status.Error(codes.PermissionDenied, "Not authenticated")
	}

	group, exists := s.groups.Get(req.GroupId)
	if group == nil || !exists {
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

func (s *server) broadcast(update *pb.GroupsUpdate) {
	for _, ch := range s.groupsSubscribers.Snapshot() {
		select {
		case ch <- update:
		default:
			// Channel full, skip
		}
	}
}

func (s *server) broadcastApplication(groupId string, update *pb.GroupApplicationUpdate) {
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

func (s *server) saveGroup(ctx context.Context, group *pb.Group) error {
	// Save to database
	return nil
}

func (s *server) deleteGroup(ctx context.Context, groupId string) error {
	// Save to database
	return nil
}
