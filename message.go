package liftbridge

import (
	"bytes"
	"hash/crc32"
	"sync"
	"time"

	"github.com/liftbridge-io/liftbridge-api/go"
)

var (
	envelopeCookie        = []byte("LIFT")
	envelopeCookieLen     = len(envelopeCookie)
	partitionByKey        = new(keyPartitioner)
	partitionByRoundRobin = newRoundRobinPartitioner()
	hasher                = crc32.ChecksumIEEE
)

// AckPolicy controls the behavior of message acknowledgements.
type AckPolicy int32

func (a AckPolicy) toProto() proto.AckPolicy {
	return proto.AckPolicy(a)
}

// Message being sent to or received from a Liftbridge stream.
type Message interface {
	// Offset is a monotonic message sequence in the stream partition.
	Offset() int64

	// Key is an optional label set on a Message, useful for partitioning and
	// stream compaction.
	Key() []byte

	// Value is the Message payload.
	Value() []byte

	// Timestamp is the time the Message was received by the server.
	Timestamp() time.Time

	// Subject is the NATS subject the Message was received on.
	Subject() string

	// ReplySubject is the NATS reply subject on the Message, if any.
	ReplySubject() string

	// Headers is a set of key-value pairs.
	Headers() map[string][]byte

	// AckInbox is the NATS subject used to publish acks to.
	AckInbox() string

	// CorrelationID is a user-supplied value to correlate acks to publishes.
	CorrelationID() string

	// AckPolicy controls the behavior of acks.
	AckPolicy() AckPolicy
}

// protoMessage is a Message backed by protobuf.
type protoMessage struct {
	msg *proto.Message
}

func newProtoMessage(msg *proto.Message) Message {
	if msg == nil {
		return nil
	}
	return &protoMessage{msg}
}

// Offset is a monotonic message sequence in the stream partition.
func (p *protoMessage) Offset() int64 {
	return p.msg.GetOffset()
}

// Key is an optional label set on a Message, useful for partitioning and
// stream compaction.
func (p *protoMessage) Key() []byte {
	return p.msg.GetKey()
}

// Value is the Message payload.
func (p *protoMessage) Value() []byte {
	return p.msg.GetValue()
}

// Timestamp is the time the Message was received by the server.
func (p *protoMessage) Timestamp() time.Time {
	return time.Unix(0, p.msg.GetTimestamp())
}

// Subject is the NATS subject the Message was received on.
func (p *protoMessage) Subject() string {
	return p.msg.GetSubject()
}

// ReplySubject is the NATS reply subject on the Message, if any.
func (p *protoMessage) ReplySubject() string {
	return p.msg.GetReplySubject()
}

// Headers is a set of key-value pairs.
func (p *protoMessage) Headers() map[string][]byte {
	return p.msg.GetHeaders()
}

// AckInbox is the NATS subject used to publish acks to.
func (p *protoMessage) AckInbox() string {
	return p.msg.GetAckInbox()
}

// CorrelationID is a user-supplied value to correlate acks to publishes.
func (p *protoMessage) CorrelationID() string {
	return p.msg.GetCorrelationId()
}

// AckPolicy controls the behavior of acks.
func (p *protoMessage) AckPolicy() AckPolicy {
	return AckPolicy(p.msg.GetAckPolicy())
}

// Ack represents an acknowledgement that a message was committed to a stream
// partition.
type Ack interface {
	// Stream the Message was received on.
	Stream() string

	// PartitionSubject is the NATS subject the partition is attached to.
	PartitionSubject() string

	// MessageSubject is the NATS subject the message was received on.
	MessageSubject() string

	// Offset is the partition offset the message was committed to.
	Offset() int64

	// AckInbox is the NATS subject the ack was published to.
	AckInbox() string

	// CorrelationID is the user-supplied value from the message.
	CorrelationID() string

	// AckPolicy sent on the message.
	AckPolicy() AckPolicy
}

// protoAck is an Ack backed by protobuf.
type protoAck struct {
	ack *proto.Ack
}

func newProtoAck(ack *proto.Ack) Ack {
	if ack == nil {
		return nil
	}
	return &protoAck{ack}
}

// Stream the Message was received on.
func (p *protoAck) Stream() string {
	return p.ack.GetStream()
}

// PartitionSubject is the NATS subject the partition is attached to.
func (p *protoAck) PartitionSubject() string {
	return p.ack.GetPartitionSubject()
}

// MessageSubject is the NATS subject the message was received on.
func (p *protoAck) MessageSubject() string {
	return p.ack.GetMsgSubject()
}

// Offset is the partition offset the message was committed to.
func (p *protoAck) Offset() int64 {
	return p.ack.GetOffset()
}

// AckInbox is the NATS subject the ack was published to.
func (p *protoAck) AckInbox() string {
	return p.ack.GetAckInbox()
}

// CorrelationID is the user-supplied value from the message.
func (p *protoAck) CorrelationID() string {
	return p.ack.GetCorrelationId()
}

// AckPolicy sent on the message.
func (p *protoAck) AckPolicy() AckPolicy {
	return AckPolicy(p.ack.GetAckPolicy())
}

// Partitioner is used to map a message to a stream partition.
type Partitioner interface {
	// Partition computes the partition number for a given message.
	Partition(stream string, key, value []byte, metadata *Metadata) int32
}

// keyPartitioner is an implementation of Partitioner which partitions messages
// based on a hash of the key.
type keyPartitioner struct{}

// Partition computes the partition number for a given message by hashing the
// key and modding by the number of stream partitions.
func (k *keyPartitioner) Partition(stream string, key, value []byte, metadata *Metadata) int32 {
	if key == nil {
		key = []byte("")
	}

	partitions := metadata.PartitionCountForStream(stream)
	if partitions == 0 {
		return 0
	}

	return int32(hasher(key)) % partitions
}

type streamCounter struct {
	sync.Mutex
	count int32
}

// roundRobinPartitioner is an implementation of Partitioner which partitions
// messages in a round-robin fashion.
type roundRobinPartitioner struct {
	sync.Mutex
	streamCounterMap map[string]*streamCounter
}

func newRoundRobinPartitioner() Partitioner {
	return &roundRobinPartitioner{
		streamCounterMap: make(map[string]*streamCounter),
	}
}

// Partition computes the partition number for a given message in a round-robin
// fashion by atomically incrementing a counter for the message stream and
// modding by the number of stream partitions.
func (r *roundRobinPartitioner) Partition(stream string, key, value []byte, metadata *Metadata) int32 {
	partitions := metadata.PartitionCountForStream(stream)
	if partitions == 0 {
		return 0
	}
	r.Lock()
	counter, ok := r.streamCounterMap[stream]
	if !ok {
		counter = new(streamCounter)
		r.streamCounterMap[stream] = counter
	}
	r.Unlock()
	counter.Lock()
	count := counter.count
	counter.count++
	counter.Unlock()
	return count % partitions
}

// MessageOptions are used to configure optional settings for a Message.
type MessageOptions struct {
	// Key to set on the Message. If Liftbridge has stream compaction enabled,
	// the stream will retain only the last value for each key.
	Key []byte

	// AckInbox sets the NATS subject Liftbridge should publish the Message ack
	// to. If it's not set, Liftbridge will not send an ack.
	AckInbox string

	// CorrelationID sets the identifier used to correlate an ack with the
	// published Message. If it's not set, the ack will not have a correlation
	// id.
	CorrelationID string

	// AckPolicy controls the behavior of Message acks sent by the server. By
	// default, Liftbridge will send an ack when the stream leader has written
	// the Message to its write-ahead log.
	AckPolicy AckPolicy

	// Headers are key-value pairs to set on the Message.
	Headers map[string][]byte

	// Partitioner specifies the strategy for mapping a Message to a stream
	// partition.
	Partitioner Partitioner

	// Partition specifies the stream partition to publish the Message to. If
	// this is set, any Partitioner will not be used. This is a pointer to
	// allow distinguishing between unset and 0.
	Partition *int32
}

// MessageOption is a function on the MessageOptions for a Message. These are
// used to configure particular optional Message fields.
type MessageOption func(*MessageOptions)

// Key is a MessageOption to set the key on a Message. If Liftbridge has stream
// compaction enabled, the stream will retain only the last value for each key.
func Key(key []byte) MessageOption {
	return func(o *MessageOptions) {
		o.Key = key
	}
}

// AckInbox is a MessageOption to set the NATS subject Liftbridge should
// publish the Message ack to. If it's not set, Liftbridge will not send an
// ack.
func AckInbox(ackInbox string) MessageOption {
	return func(o *MessageOptions) {
		o.AckInbox = ackInbox
	}
}

// CorrelationID is a MessageOption to set the identifier used to correlate an
// ack with the published Message. If it's not set, the ack will not have a
// correlation id.
func CorrelationID(correlationID string) MessageOption {
	return func(o *MessageOptions) {
		o.CorrelationID = correlationID
	}
}

// AckPolicyLeader is a MessageOption that sets the AckPolicy of the Message to
// LEADER. This means the Message ack will be sent when the stream leader has
// written it to its write-ahead log.
func AckPolicyLeader() MessageOption {
	return func(o *MessageOptions) {
		o.AckPolicy = AckPolicy(proto.AckPolicy_LEADER)
	}
}

// AckPolicyAll is a MessageOption that sets the AckPolicy of the Message to
// ALL. This means the Message ack will be sent when the message has been
// written to all replicas.
func AckPolicyAll() MessageOption {
	return func(o *MessageOptions) {
		o.AckPolicy = AckPolicy(proto.AckPolicy_ALL)
	}
}

// AckPolicyNone is a MessageOption that sets the AckPolicy of the Message to
// NONE. This means no ack will be sent.
func AckPolicyNone() MessageOption {
	return func(o *MessageOptions) {
		o.AckPolicy = AckPolicy(proto.AckPolicy_NONE)
	}
}

// Header is a MessageOption that adds a single header to the Message. This may
// overwrite previously set headers.
func Header(name string, value []byte) MessageOption {
	return func(o *MessageOptions) {
		o.Headers[name] = value
	}
}

// Headers is a MessageOption that adds a set of headers to the Message. This
// may overwrite previously set headers.
func Headers(headers map[string][]byte) MessageOption {
	return func(o *MessageOptions) {
		for name, value := range headers {
			o.Headers[name] = value
		}
	}
}

// ToPartition is a MessageOption that specifies the stream partition to
// publish the Message to. If this is set, any Partitioner will not be used.
func ToPartition(partition int32) MessageOption {
	return func(o *MessageOptions) {
		o.Partition = &partition
	}
}

// PartitionBy is a MessageOption that specifies a Partitioner used to map
// Messages to stream partitions.
func PartitionBy(partitioner Partitioner) MessageOption {
	return func(o *MessageOptions) {
		o.Partitioner = partitioner
	}
}

// PartitionByKey is a MessageOption that maps Messages to stream partitions
// based on a hash of the Message key. This computes the partition number for a
// given message by hashing the key and modding by the number of partitions for
// the first stream found with the subject of the published message. This does
// not work with streams containing wildcards in their subjects, e.g. "foo.*",
// since this matches on the subject literal of the published message. This
// also has undefined behavior if there are multiple streams for the given
// subject.
func PartitionByKey() MessageOption {
	return PartitionBy(partitionByKey)
}

// PartitionByRoundRobin is a MessageOption that maps Messages to stream
// partitions in a round-robin fashion. This computes the partition number for
// a given message by atomically incrementing a counter for the message subject
// and modding by the number of partitions for the first stream found with the
// subject. This does not work with streams containing wildcards in their
// subjects, e.g. "foo.*", since this matches on the subject literal of the
// published message. This also has undefined behavior if there are multiple
// streams for the given subject.
func PartitionByRoundRobin() MessageOption {
	return PartitionBy(partitionByRoundRobin)
}

// NewMessage returns a serialized message for the given payload and options.
func NewMessage(value []byte, options ...MessageOption) []byte {
	opts := &MessageOptions{Headers: make(map[string][]byte)}
	for _, opt := range options {
		opt(opts)
	}

	msg, err := (&proto.Message{
		Key:           opts.Key,
		Value:         value,
		AckInbox:      opts.AckInbox,
		CorrelationId: opts.CorrelationID,
		AckPolicy:     opts.AckPolicy.toProto(),
	}).Marshal()
	if err != nil {
		panic(err)
	}

	buf := make([]byte, envelopeCookieLen+len(msg))
	copy(buf[0:], envelopeCookie)
	copy(buf[envelopeCookieLen:], msg)
	return buf
}

// UnmarshalAck deserializes an Ack from the given byte slice. It returns an
// error if the given data is not actually an Ack.
func UnmarshalAck(data []byte) (Ack, error) {
	var (
		ack = &proto.Ack{}
		err = ack.Unmarshal(data)
	)
	return newProtoAck(ack), err
}

// UnmarshalMessage deserializes a message from the given byte slice.  It
// returns a bool indicating if the given data was actually a Message or not.
func UnmarshalMessage(data []byte) (Message, bool) {
	if len(data) <= envelopeCookieLen {
		return nil, false
	}
	if !bytes.Equal(data[:envelopeCookieLen], envelopeCookie) {
		return nil, false
	}
	var (
		msg = &proto.Message{}
		err = msg.Unmarshal(data[envelopeCookieLen:])
	)
	if err != nil {
		return nil, false
	}
	return newProtoMessage(msg), true
}
