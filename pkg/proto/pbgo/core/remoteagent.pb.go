// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.34.0
// 	protoc        v5.26.1
// source: datadog/remoteagent/remoteagent.proto

package core

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type StatusSection struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Fields map[string]string `protobuf:"bytes,1,rep,name=fields,proto3" json:"fields,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"bytes,2,opt,name=value,proto3"`
}

func (x *StatusSection) Reset() {
	*x = StatusSection{}
	if protoimpl.UnsafeEnabled {
		mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *StatusSection) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*StatusSection) ProtoMessage() {}

func (x *StatusSection) ProtoReflect() protoreflect.Message {
	mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use StatusSection.ProtoReflect.Descriptor instead.
func (*StatusSection) Descriptor() ([]byte, []int) {
	return file_datadog_remoteagent_remoteagent_proto_rawDescGZIP(), []int{0}
}

func (x *StatusSection) GetFields() map[string]string {
	if x != nil {
		return x.Fields
	}
	return nil
}

type RegisterRemoteAgentRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Unique ID of the remote agent.
	//
	// SHOULD be semi-human-readable, with a unique component, such as the process name followed by a UUID:
	// otel-agent-0192de13-3d66-7cbc-9b4f-1b74f7b8a467.
	Id string `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
	// Human-friendly display name of the remote agent.
	//
	// SHOULD be the common name for the remote agent, such as OpenTelemetry Collector Agent.
	DisplayName string `protobuf:"bytes,2,opt,name=display_name,json=displayName,proto3" json:"display_name,omitempty"`
	// gRPC endpoint address to reach the remote agent at.
	//
	// MUST be a valid gRPC endpoint address, such as "localhost:4317"
	// MUST be exposing the `RemoteAgent` service.
	// MUST be secured with TLS, and SHOULD present a valid certificate where possible.
	ApiEndpoint string `protobuf:"bytes,3,opt,name=api_endpoint,json=apiEndpoint,proto3" json:"api_endpoint,omitempty"`
	// Authentication token to be used when connecting to the remote agent's gRPC endpoint.
	//
	// The remote agent's gRPC endpoint MUST check that this authentication token was provided as a bearer token in all
	// requests made to the endpoint. If the token is not provided, the remote agent SHOULD reject the request.
	//
	// SHOULD be a unique string value that is generated randomly before a remote agent registers itself for the first time.
	AuthToken string `protobuf:"bytes,4,opt,name=auth_token,json=authToken,proto3" json:"auth_token,omitempty"`
}

func (x *RegisterRemoteAgentRequest) Reset() {
	*x = RegisterRemoteAgentRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *RegisterRemoteAgentRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RegisterRemoteAgentRequest) ProtoMessage() {}

func (x *RegisterRemoteAgentRequest) ProtoReflect() protoreflect.Message {
	mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RegisterRemoteAgentRequest.ProtoReflect.Descriptor instead.
func (*RegisterRemoteAgentRequest) Descriptor() ([]byte, []int) {
	return file_datadog_remoteagent_remoteagent_proto_rawDescGZIP(), []int{1}
}

func (x *RegisterRemoteAgentRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *RegisterRemoteAgentRequest) GetDisplayName() string {
	if x != nil {
		return x.DisplayName
	}
	return ""
}

func (x *RegisterRemoteAgentRequest) GetApiEndpoint() string {
	if x != nil {
		return x.ApiEndpoint
	}
	return ""
}

func (x *RegisterRemoteAgentRequest) GetAuthToken() string {
	if x != nil {
		return x.AuthToken
	}
	return ""
}

type RegisterRemoteAgentResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Recommended refresh interval for the remote agent.
	//
	// This is the interval at which the remote agent should call the RegisterRemoteAgent RPC in order to assert that the
	// remote agent is live and healthy.
	//
	// The remote agent SHOULD refresh its status every `recommended_refresh_interval_secs` seconds.
	RecommendedRefreshIntervalSecs uint32 `protobuf:"varint,1,opt,name=recommended_refresh_interval_secs,json=recommendedRefreshIntervalSecs,proto3" json:"recommended_refresh_interval_secs,omitempty"`
}

func (x *RegisterRemoteAgentResponse) Reset() {
	*x = RegisterRemoteAgentResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *RegisterRemoteAgentResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RegisterRemoteAgentResponse) ProtoMessage() {}

func (x *RegisterRemoteAgentResponse) ProtoReflect() protoreflect.Message {
	mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RegisterRemoteAgentResponse.ProtoReflect.Descriptor instead.
func (*RegisterRemoteAgentResponse) Descriptor() ([]byte, []int) {
	return file_datadog_remoteagent_remoteagent_proto_rawDescGZIP(), []int{2}
}

func (x *RegisterRemoteAgentResponse) GetRecommendedRefreshIntervalSecs() uint32 {
	if x != nil {
		return x.RecommendedRefreshIntervalSecs
	}
	return 0
}

type GetStatusDetailsRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *GetStatusDetailsRequest) Reset() {
	*x = GetStatusDetailsRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GetStatusDetailsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetStatusDetailsRequest) ProtoMessage() {}

func (x *GetStatusDetailsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetStatusDetailsRequest.ProtoReflect.Descriptor instead.
func (*GetStatusDetailsRequest) Descriptor() ([]byte, []int) {
	return file_datadog_remoteagent_remoteagent_proto_rawDescGZIP(), []int{3}
}

type GetStatusDetailsResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Main status detail section.
	//
	// Generally reserved for high-level details such as version, uptime, configuration flags, etc.
	MainSection *StatusSection `protobuf:"bytes,1,opt,name=main_section,json=mainSection,proto3" json:"main_section,omitempty"`
	// Named status detail sections.
	//
	// Generally reserved for specific (sub)component details, such as the status of a specific feature or integration, etc.
	NamedSections map[string]*StatusSection `protobuf:"bytes,2,rep,name=named_sections,json=namedSections,proto3" json:"named_sections,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"bytes,2,opt,name=value,proto3"`
}

func (x *GetStatusDetailsResponse) Reset() {
	*x = GetStatusDetailsResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[4]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GetStatusDetailsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetStatusDetailsResponse) ProtoMessage() {}

func (x *GetStatusDetailsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[4]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetStatusDetailsResponse.ProtoReflect.Descriptor instead.
func (*GetStatusDetailsResponse) Descriptor() ([]byte, []int) {
	return file_datadog_remoteagent_remoteagent_proto_rawDescGZIP(), []int{4}
}

func (x *GetStatusDetailsResponse) GetMainSection() *StatusSection {
	if x != nil {
		return x.MainSection
	}
	return nil
}

func (x *GetStatusDetailsResponse) GetNamedSections() map[string]*StatusSection {
	if x != nil {
		return x.NamedSections
	}
	return nil
}

type GetFlareFilesRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *GetFlareFilesRequest) Reset() {
	*x = GetFlareFilesRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[5]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GetFlareFilesRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetFlareFilesRequest) ProtoMessage() {}

func (x *GetFlareFilesRequest) ProtoReflect() protoreflect.Message {
	mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[5]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetFlareFilesRequest.ProtoReflect.Descriptor instead.
func (*GetFlareFilesRequest) Descriptor() ([]byte, []int) {
	return file_datadog_remoteagent_remoteagent_proto_rawDescGZIP(), []int{5}
}

type GetFlareFilesResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Set of files to add to the flare.
	//
	// The key is the name of the file, and the value is the contents of the file.
	//
	// The key SHOULD be an ASCII string with no path separators (`/`), and will be sanitized as necessary to ensure it can be
	// used as a valid filename. The key SHOULD have a file extension that is applicable to the file contents, such as
	// `.yaml` for YAML data.
	Files map[string][]byte `protobuf:"bytes,1,rep,name=files,proto3" json:"files,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"bytes,2,opt,name=value,proto3"`
}

func (x *GetFlareFilesResponse) Reset() {
	*x = GetFlareFilesResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[6]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GetFlareFilesResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetFlareFilesResponse) ProtoMessage() {}

func (x *GetFlareFilesResponse) ProtoReflect() protoreflect.Message {
	mi := &file_datadog_remoteagent_remoteagent_proto_msgTypes[6]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetFlareFilesResponse.ProtoReflect.Descriptor instead.
func (*GetFlareFilesResponse) Descriptor() ([]byte, []int) {
	return file_datadog_remoteagent_remoteagent_proto_rawDescGZIP(), []int{6}
}

func (x *GetFlareFilesResponse) GetFiles() map[string][]byte {
	if x != nil {
		return x.Files
	}
	return nil
}

var File_datadog_remoteagent_remoteagent_proto protoreflect.FileDescriptor

var file_datadog_remoteagent_remoteagent_proto_rawDesc = []byte{
	0x0a, 0x25, 0x64, 0x61, 0x74, 0x61, 0x64, 0x6f, 0x67, 0x2f, 0x72, 0x65, 0x6d, 0x6f, 0x74, 0x65,
	0x61, 0x67, 0x65, 0x6e, 0x74, 0x2f, 0x72, 0x65, 0x6d, 0x6f, 0x74, 0x65, 0x61, 0x67, 0x65, 0x6e,
	0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x13, 0x64, 0x61, 0x74, 0x61, 0x64, 0x6f, 0x67,
	0x2e, 0x72, 0x65, 0x6d, 0x6f, 0x74, 0x65, 0x61, 0x67, 0x65, 0x6e, 0x74, 0x22, 0x92, 0x01, 0x0a,
	0x0d, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x53, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x12, 0x46,
	0x0a, 0x06, 0x66, 0x69, 0x65, 0x6c, 0x64, 0x73, 0x18, 0x01, 0x20, 0x03, 0x28, 0x0b, 0x32, 0x2e,
	0x2e, 0x64, 0x61, 0x74, 0x61, 0x64, 0x6f, 0x67, 0x2e, 0x72, 0x65, 0x6d, 0x6f, 0x74, 0x65, 0x61,
	0x67, 0x65, 0x6e, 0x74, 0x2e, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x53, 0x65, 0x63, 0x74, 0x69,
	0x6f, 0x6e, 0x2e, 0x46, 0x69, 0x65, 0x6c, 0x64, 0x73, 0x45, 0x6e, 0x74, 0x72, 0x79, 0x52, 0x06,
	0x66, 0x69, 0x65, 0x6c, 0x64, 0x73, 0x1a, 0x39, 0x0a, 0x0b, 0x46, 0x69, 0x65, 0x6c, 0x64, 0x73,
	0x45, 0x6e, 0x74, 0x72, 0x79, 0x12, 0x10, 0x0a, 0x03, 0x6b, 0x65, 0x79, 0x18, 0x01, 0x20, 0x01,
	0x28, 0x09, 0x52, 0x03, 0x6b, 0x65, 0x79, 0x12, 0x14, 0x0a, 0x05, 0x76, 0x61, 0x6c, 0x75, 0x65,
	0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x05, 0x76, 0x61, 0x6c, 0x75, 0x65, 0x3a, 0x02, 0x38,
	0x01, 0x22, 0x91, 0x01, 0x0a, 0x1a, 0x52, 0x65, 0x67, 0x69, 0x73, 0x74, 0x65, 0x72, 0x52, 0x65,
	0x6d, 0x6f, 0x74, 0x65, 0x41, 0x67, 0x65, 0x6e, 0x74, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74,
	0x12, 0x0e, 0x0a, 0x02, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x02, 0x69, 0x64,
	0x12, 0x21, 0x0a, 0x0c, 0x64, 0x69, 0x73, 0x70, 0x6c, 0x61, 0x79, 0x5f, 0x6e, 0x61, 0x6d, 0x65,
	0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0b, 0x64, 0x69, 0x73, 0x70, 0x6c, 0x61, 0x79, 0x4e,
	0x61, 0x6d, 0x65, 0x12, 0x21, 0x0a, 0x0c, 0x61, 0x70, 0x69, 0x5f, 0x65, 0x6e, 0x64, 0x70, 0x6f,
	0x69, 0x6e, 0x74, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0b, 0x61, 0x70, 0x69, 0x45, 0x6e,
	0x64, 0x70, 0x6f, 0x69, 0x6e, 0x74, 0x12, 0x1d, 0x0a, 0x0a, 0x61, 0x75, 0x74, 0x68, 0x5f, 0x74,
	0x6f, 0x6b, 0x65, 0x6e, 0x18, 0x04, 0x20, 0x01, 0x28, 0x09, 0x52, 0x09, 0x61, 0x75, 0x74, 0x68,
	0x54, 0x6f, 0x6b, 0x65, 0x6e, 0x22, 0x68, 0x0a, 0x1b, 0x52, 0x65, 0x67, 0x69, 0x73, 0x74, 0x65,
	0x72, 0x52, 0x65, 0x6d, 0x6f, 0x74, 0x65, 0x41, 0x67, 0x65, 0x6e, 0x74, 0x52, 0x65, 0x73, 0x70,
	0x6f, 0x6e, 0x73, 0x65, 0x12, 0x49, 0x0a, 0x21, 0x72, 0x65, 0x63, 0x6f, 0x6d, 0x6d, 0x65, 0x6e,
	0x64, 0x65, 0x64, 0x5f, 0x72, 0x65, 0x66, 0x72, 0x65, 0x73, 0x68, 0x5f, 0x69, 0x6e, 0x74, 0x65,
	0x72, 0x76, 0x61, 0x6c, 0x5f, 0x73, 0x65, 0x63, 0x73, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0d, 0x52,
	0x1e, 0x72, 0x65, 0x63, 0x6f, 0x6d, 0x6d, 0x65, 0x6e, 0x64, 0x65, 0x64, 0x52, 0x65, 0x66, 0x72,
	0x65, 0x73, 0x68, 0x49, 0x6e, 0x74, 0x65, 0x72, 0x76, 0x61, 0x6c, 0x53, 0x65, 0x63, 0x73, 0x22,
	0x19, 0x0a, 0x17, 0x47, 0x65, 0x74, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x44, 0x65, 0x74, 0x61,
	0x69, 0x6c, 0x73, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x22, 0xb0, 0x02, 0x0a, 0x18, 0x47,
	0x65, 0x74, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x44, 0x65, 0x74, 0x61, 0x69, 0x6c, 0x73, 0x52,
	0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x45, 0x0a, 0x0c, 0x6d, 0x61, 0x69, 0x6e, 0x5f,
	0x73, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x22, 0x2e,
	0x64, 0x61, 0x74, 0x61, 0x64, 0x6f, 0x67, 0x2e, 0x72, 0x65, 0x6d, 0x6f, 0x74, 0x65, 0x61, 0x67,
	0x65, 0x6e, 0x74, 0x2e, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x53, 0x65, 0x63, 0x74, 0x69, 0x6f,
	0x6e, 0x52, 0x0b, 0x6d, 0x61, 0x69, 0x6e, 0x53, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x12, 0x67,
	0x0a, 0x0e, 0x6e, 0x61, 0x6d, 0x65, 0x64, 0x5f, 0x73, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x73,
	0x18, 0x02, 0x20, 0x03, 0x28, 0x0b, 0x32, 0x40, 0x2e, 0x64, 0x61, 0x74, 0x61, 0x64, 0x6f, 0x67,
	0x2e, 0x72, 0x65, 0x6d, 0x6f, 0x74, 0x65, 0x61, 0x67, 0x65, 0x6e, 0x74, 0x2e, 0x47, 0x65, 0x74,
	0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x44, 0x65, 0x74, 0x61, 0x69, 0x6c, 0x73, 0x52, 0x65, 0x73,
	0x70, 0x6f, 0x6e, 0x73, 0x65, 0x2e, 0x4e, 0x61, 0x6d, 0x65, 0x64, 0x53, 0x65, 0x63, 0x74, 0x69,
	0x6f, 0x6e, 0x73, 0x45, 0x6e, 0x74, 0x72, 0x79, 0x52, 0x0d, 0x6e, 0x61, 0x6d, 0x65, 0x64, 0x53,
	0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x73, 0x1a, 0x64, 0x0a, 0x12, 0x4e, 0x61, 0x6d, 0x65, 0x64,
	0x53, 0x65, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x73, 0x45, 0x6e, 0x74, 0x72, 0x79, 0x12, 0x10, 0x0a,
	0x03, 0x6b, 0x65, 0x79, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x03, 0x6b, 0x65, 0x79, 0x12,
	0x38, 0x0a, 0x05, 0x76, 0x61, 0x6c, 0x75, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x22,
	0x2e, 0x64, 0x61, 0x74, 0x61, 0x64, 0x6f, 0x67, 0x2e, 0x72, 0x65, 0x6d, 0x6f, 0x74, 0x65, 0x61,
	0x67, 0x65, 0x6e, 0x74, 0x2e, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x53, 0x65, 0x63, 0x74, 0x69,
	0x6f, 0x6e, 0x52, 0x05, 0x76, 0x61, 0x6c, 0x75, 0x65, 0x3a, 0x02, 0x38, 0x01, 0x22, 0x16, 0x0a,
	0x14, 0x47, 0x65, 0x74, 0x46, 0x6c, 0x61, 0x72, 0x65, 0x46, 0x69, 0x6c, 0x65, 0x73, 0x52, 0x65,
	0x71, 0x75, 0x65, 0x73, 0x74, 0x22, 0x9e, 0x01, 0x0a, 0x15, 0x47, 0x65, 0x74, 0x46, 0x6c, 0x61,
	0x72, 0x65, 0x46, 0x69, 0x6c, 0x65, 0x73, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12,
	0x4b, 0x0a, 0x05, 0x66, 0x69, 0x6c, 0x65, 0x73, 0x18, 0x01, 0x20, 0x03, 0x28, 0x0b, 0x32, 0x35,
	0x2e, 0x64, 0x61, 0x74, 0x61, 0x64, 0x6f, 0x67, 0x2e, 0x72, 0x65, 0x6d, 0x6f, 0x74, 0x65, 0x61,
	0x67, 0x65, 0x6e, 0x74, 0x2e, 0x47, 0x65, 0x74, 0x46, 0x6c, 0x61, 0x72, 0x65, 0x46, 0x69, 0x6c,
	0x65, 0x73, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x2e, 0x46, 0x69, 0x6c, 0x65, 0x73,
	0x45, 0x6e, 0x74, 0x72, 0x79, 0x52, 0x05, 0x66, 0x69, 0x6c, 0x65, 0x73, 0x1a, 0x38, 0x0a, 0x0a,
	0x46, 0x69, 0x6c, 0x65, 0x73, 0x45, 0x6e, 0x74, 0x72, 0x79, 0x12, 0x10, 0x0a, 0x03, 0x6b, 0x65,
	0x79, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x03, 0x6b, 0x65, 0x79, 0x12, 0x14, 0x0a, 0x05,
	0x76, 0x61, 0x6c, 0x75, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x05, 0x76, 0x61, 0x6c,
	0x75, 0x65, 0x3a, 0x02, 0x38, 0x01, 0x42, 0x15, 0x5a, 0x13, 0x70, 0x6b, 0x67, 0x2f, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x2f, 0x70, 0x62, 0x67, 0x6f, 0x2f, 0x63, 0x6f, 0x72, 0x65, 0x62, 0x06, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_datadog_remoteagent_remoteagent_proto_rawDescOnce sync.Once
	file_datadog_remoteagent_remoteagent_proto_rawDescData = file_datadog_remoteagent_remoteagent_proto_rawDesc
)

func file_datadog_remoteagent_remoteagent_proto_rawDescGZIP() []byte {
	file_datadog_remoteagent_remoteagent_proto_rawDescOnce.Do(func() {
		file_datadog_remoteagent_remoteagent_proto_rawDescData = protoimpl.X.CompressGZIP(file_datadog_remoteagent_remoteagent_proto_rawDescData)
	})
	return file_datadog_remoteagent_remoteagent_proto_rawDescData
}

var file_datadog_remoteagent_remoteagent_proto_msgTypes = make([]protoimpl.MessageInfo, 10)
var file_datadog_remoteagent_remoteagent_proto_goTypes = []interface{}{
	(*StatusSection)(nil),               // 0: datadog.remoteagent.StatusSection
	(*RegisterRemoteAgentRequest)(nil),  // 1: datadog.remoteagent.RegisterRemoteAgentRequest
	(*RegisterRemoteAgentResponse)(nil), // 2: datadog.remoteagent.RegisterRemoteAgentResponse
	(*GetStatusDetailsRequest)(nil),     // 3: datadog.remoteagent.GetStatusDetailsRequest
	(*GetStatusDetailsResponse)(nil),    // 4: datadog.remoteagent.GetStatusDetailsResponse
	(*GetFlareFilesRequest)(nil),        // 5: datadog.remoteagent.GetFlareFilesRequest
	(*GetFlareFilesResponse)(nil),       // 6: datadog.remoteagent.GetFlareFilesResponse
	nil,                                 // 7: datadog.remoteagent.StatusSection.FieldsEntry
	nil,                                 // 8: datadog.remoteagent.GetStatusDetailsResponse.NamedSectionsEntry
	nil,                                 // 9: datadog.remoteagent.GetFlareFilesResponse.FilesEntry
}
var file_datadog_remoteagent_remoteagent_proto_depIdxs = []int32{
	7, // 0: datadog.remoteagent.StatusSection.fields:type_name -> datadog.remoteagent.StatusSection.FieldsEntry
	0, // 1: datadog.remoteagent.GetStatusDetailsResponse.main_section:type_name -> datadog.remoteagent.StatusSection
	8, // 2: datadog.remoteagent.GetStatusDetailsResponse.named_sections:type_name -> datadog.remoteagent.GetStatusDetailsResponse.NamedSectionsEntry
	9, // 3: datadog.remoteagent.GetFlareFilesResponse.files:type_name -> datadog.remoteagent.GetFlareFilesResponse.FilesEntry
	0, // 4: datadog.remoteagent.GetStatusDetailsResponse.NamedSectionsEntry.value:type_name -> datadog.remoteagent.StatusSection
	5, // [5:5] is the sub-list for method output_type
	5, // [5:5] is the sub-list for method input_type
	5, // [5:5] is the sub-list for extension type_name
	5, // [5:5] is the sub-list for extension extendee
	0, // [0:5] is the sub-list for field type_name
}

func init() { file_datadog_remoteagent_remoteagent_proto_init() }
func file_datadog_remoteagent_remoteagent_proto_init() {
	if File_datadog_remoteagent_remoteagent_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_datadog_remoteagent_remoteagent_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*StatusSection); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_datadog_remoteagent_remoteagent_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*RegisterRemoteAgentRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_datadog_remoteagent_remoteagent_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*RegisterRemoteAgentResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_datadog_remoteagent_remoteagent_proto_msgTypes[3].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GetStatusDetailsRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_datadog_remoteagent_remoteagent_proto_msgTypes[4].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GetStatusDetailsResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_datadog_remoteagent_remoteagent_proto_msgTypes[5].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GetFlareFilesRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_datadog_remoteagent_remoteagent_proto_msgTypes[6].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GetFlareFilesResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_datadog_remoteagent_remoteagent_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   10,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_datadog_remoteagent_remoteagent_proto_goTypes,
		DependencyIndexes: file_datadog_remoteagent_remoteagent_proto_depIdxs,
		MessageInfos:      file_datadog_remoteagent_remoteagent_proto_msgTypes,
	}.Build()
	File_datadog_remoteagent_remoteagent_proto = out.File
	file_datadog_remoteagent_remoteagent_proto_rawDesc = nil
	file_datadog_remoteagent_remoteagent_proto_goTypes = nil
	file_datadog_remoteagent_remoteagent_proto_depIdxs = nil
}