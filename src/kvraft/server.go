package raftkv

import (
	"bytes"
	"labgob"
	"labrpc"
	"log"
	"raft"
	"sync"
	"time"
)

const Debug = 1

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug > 0 {
		log.Printf(format, a...)
	}
	return
}

//必须注册，否则报空指针异常
func init() {
	labgob.Register(GetArgs{})
	labgob.Register(PutAppendArgs{})
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}


type Op struct {
	// Your definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
}

type NotifyMsg struct {
	Err         Err
	Value       string
}

type KVServer struct {
	sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg

	persister *raft.Persister
	maxraftstate int // snapshot if log grows this big
	shutdown 		chan struct{}
	data			map[string]string
	cache			map[int64]int
	notifyChanMap 	map[int]chan NotifyMsg
}

func (kv *KVServer) snapshot(lastCommandIndex int) {
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(kv.data)
	e.Encode(kv.cache)
	snapshot := w.Bytes()
	//需要修改log和lastincludedindex，所以此函数在raft层实现
	kv.rf.PersistAndSaveSnapshot(lastCommandIndex, snapshot)
}

func (kv *KVServer) readSnapshot() {
	snapshot := kv.persister.ReadSnapshot()
	if snapshot == nil || len(snapshot) < 1 {
		return
	}
	r := bytes.NewBuffer(snapshot)
	d := labgob.NewDecoder(r)
	if d.Decode(&kv.data) != nil ||
		d.Decode(&kv.cache) != nil {
		log.Fatal("error while unmarshal snapshot.")
	}

}

func(kv *KVServer) snapshotIfNeed(lastCommandIndex int) {
	if kv.maxraftstate != -1 && kv.persister.RaftStateSize() >= kv.maxraftstate {
		DPrintf("trigger snapshot")
		kv.rf.TestFlag = true
		kv.snapshot(lastCommandIndex)
	}

}

func (kv *KVServer) notifyIfPresent(index int, reply NotifyMsg) {
	if ch, ok := kv.notifyChanMap[index]; ok {
		ch <- reply
		delete(kv.notifyChanMap, index)
	}
}

func (kv *KVServer) Start(command interface{}) (Err, string) {
	//todo
	index, _, ok := kv.rf.Start(command)
	if !ok {
		return ErrWrongLeader, ""
	}
	kv.Lock()
	notifyCh := make(chan NotifyMsg)
	kv.notifyChanMap[index] = notifyCh
	kv.Unlock()
	select {
	case msg := <-notifyCh:
		//DPrintf("%v notifyCh received msg.", kv.me)
		return msg.Err, msg.Value
	//必须设置超时，否则会永久阻塞
	//超时原因可能是由于网络分区没有得到majority同意
	case <-time.After(StartTimeoutInterval):
		kv.Lock()
		delete(kv.notifyChanMap, index)
		kv.Unlock()
		DPrintf("%v notifyCh received msg timeout.", kv.me)
		return ErrTimeout, ""
	}
}

func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	reply.Err, reply.Value = kv.Start(args.copy())
}

func (kv *KVServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	reply.Err, _ = kv.Start(args.copy())
}

//
// the tester calls Kill() when a KVServer instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
//
func (kv *KVServer) Kill() {
	kv.rf.Kill()
	// Your code here, if desired.
}

func(kv *KVServer) apply(msg raft.ApplyMsg) {
	result := NotifyMsg{Err:"OK", Value:""}
	if arg, ok := msg.Command.(GetArgs); ok {
		//读操作没必要缓存和检查是否是上次retry
		result.Value = kv.data[arg.Key]
	} else if arg, ok := msg.Command.(PutAppendArgs); ok {
		if arg.Key == "13" {
			DPrintf("put %v into data", arg.Key)
		}
		if kv.cache[arg.ClientId] < arg.RequestSeq {
			if arg.Op == "Put" {
				kv.data[arg.Key] = arg.Value
			} else if arg.Op == "Append" {
				kv.data[arg.Key] += arg.Value
			}
			kv.cache[arg.ClientId] = arg.RequestSeq
		}
	} else {
		result.Err = ErrWrongLeader
	}
	//DPrintf("%v send result: to notifyCh", kv.me)
	kv.notifyIfPresent(msg.CommandIndex, result)
	kv.snapshotIfNeed(msg.CommandIndex)
}

func(kv *KVServer) run() {
	for {
		select {
		//从raft返回的消息
		case msg := <-kv.applyCh:
			//接收到此消息一定是leader
			//DPrintf("%v applyCh received %v", kv.me, msg)
			if msg.CommandValid {
				kv.apply(msg)
			} else if cmd, ok := msg.Command.(string); ok {
				if cmd == "InstallSnapshot" {
					kv.readSnapshot()
				}
			}
		case <-kv.shutdown:
			return
		}
	}
}

func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})

	kv := new(KVServer)
	kv.me = me
	kv.maxraftstate = maxraftstate
	kv.shutdown = make(chan struct{})
	kv.data = make(map[string]string)
	kv.cache = make(map[int64]int)

	kv.persister = persister
	kv.applyCh = make(chan raft.ApplyMsg)
	kv.notifyChanMap = make(map[int]chan NotifyMsg)
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)

	go kv.run()
	return kv
}
