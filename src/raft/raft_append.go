package raft

import (
	"time"
	"fmt"
	"sort"
)

func (rf *Raft) AppendEntries(args *AppendMessage, reply* AppendReply) {
	start := time.Now()
	defer calcRuntime(start, "AppendEntries")
	rf.mu.Lock()
	reply.To = args.From
	reply.From = rf.me
	reply.MsgType = getResponseType(args.MsgType)
	reply.Id = args.Id

	if !rf.checkAppend(args.From, args.Term, args.MsgType) {
		fmt.Printf("%d reject (%s) from leader: %d, term: %d, leadder term: %d\n", rf.me, getMsgName(args.MsgType),
			args.From, rf.term, args.Term)
		reply.Success = false
		reply.Term = rf.term
		reply.Commited = 0
		rf.mu.Unlock()
		return
	}

	rf.leader = args.From
	rf.lastElection = time.Now()
	rf.state = Follower
	fmt.Printf("%d(%d) access msg from %d(%d)\n", rf.me, rf.term,
			args.From, args.Term)
	if args.MsgType == MsgHeartbeat {
		fmt.Printf("%d(commit: %d, applied: %d, total: %d) access Heartbeat from %d(%d) to %d\n", rf.me, rf.raftLog.commited,
			rf.raftLog.applied, rf.raftLog.Size(), args.From, args.Commited, args.To)
		rf.handleHeartbeat(args, reply)
	} else if args.MsgType == MsgAppend {
		rf.handleAppendEntries(args, reply)
		fmt.Printf("%d(%d) access append from %d(%d) to %d\n", rf.me, rf.raftLog.commited,
			args.From, args.Commited, args.To)
	}
	if rf.raftLog.applied < rf.raftLog.commited {
		entries := rf.raftLog.GetUnApplyEntry()
		for _, e := range entries {
			m := rf.createApplyMsg(e)
			if m.CommandValid {
				fmt.Printf("%d apply an entry of log[%d]=data[%d]\n", rf.me, e.Index, m.CommandIndex)
				rf.applySM <- m
			}
			rf.raftLog.applied = e.Index
		}
		rf.raftLog.applied = rf.raftLog.commited
	}
	rf.mu.Unlock()
	rf.maybeChange()
}


func (rf *Raft) handleAppendEntries(args *AppendMessage, reply *AppendReply)  {
	reply.MsgType = MsgAppendReply
	index := rf.raftLog.GetLastIndex()
	if args.PrevLogIndex > index {
		fmt.Printf("%d(index: %d, %d) reject append entries from %d(prev index: %d)\n",
			rf.me, index, rf.term, args.From, args.PrevLogIndex)
		reply.Success = false
		//reply.Commited = index - 1
		reply.Commited = rf.raftLog.commited
		reply.Term = rf.term
		return
	}
	if rf.raftLog.MatchIndexAndTerm(args.PrevLogIndex, args.PrevLogTerm) {
		lastIndex := args.PrevLogIndex + len(args.Entries)
		conflict_idx := rf.raftLog.FindConflict(args.Entries)
		if conflict_idx == 0 {
		} else if conflict_idx <= rf.raftLog.commited {
			fmt.Printf("%d(index: %d, %d) conflict append entries from %d(prev index: %d)\n",
				rf.me, index, rf.term, args.From, args.PrevLogIndex)
			return
		} else {
			from := conflict_idx - args.PrevLogIndex - 1
			ed := len(args.Entries) - 1
			if ed >= 0 {
				fmt.Printf("%d access append from %d, append entries from %d to %d\n", rf.me, args.From, args.Entries[from].Index, args.Entries[ed].Index)
			}
			for _, e:= range args.Entries[from:] {
				rf.raftLog.Append(e)
			}
		}
		fmt.Printf("%d commit to %d -> min(%d, %d) all msg: %d -> %d, preindex :%d\n", rf.me, rf.raftLog.commited,
			args.Commited, lastIndex, index, rf.raftLog.Size(), args.PrevLogIndex)
		rf.raftLog.MaybeCommit(MinInt(args.Commited, lastIndex))
		reply.Term = rf.term
		reply.Commited = lastIndex
		reply.Success = true
	} else {
		reply.Success = false
		reply.Term = rf.term
		reply.Commited = args.PrevLogIndex - 1
		if rf.raftLog.GetLastIndex() > 2 + rf.raftLog.commited {
			reply.Commited = rf.raftLog.commited
		}
		fmt.Printf("%d(commit  %d) reject append entries from %d(prev index: %d, term: %d)\n",
			rf.me, rf.raftLog.commited, args.From, args.PrevLogIndex, args.PrevLogTerm)
		//fmt.Printf("%d(index: %d, term: %d) %d reject append entries from %d(prev index: %d, term: %d)\n",
		//	rf.me, e.Index, e.Term, rf.raftLog.commited, args.From, args.PrevLogIndex, args.PrevLogTerm)
	}
}

func (rf *Raft) handleHeartbeat(msg *AppendMessage, reply *AppendReply)  {
	reply.Success = true
	reply.Term = MaxInt(rf.term, reply.Term)
	reply.Commited = rf.raftLog.GetLastIndex()
	reply.MsgType = MsgHeartbeatReply
	rf.term = msg.Term
	if rf.raftLog.MaybeCommit(msg.Commited) {
		fmt.Printf("%d commit to %d, log length: %d, last index:%d leader : %d\n",
			rf.me, rf.raftLog.commited, rf.raftLog.Size(), rf.raftLog.GetLastIndex(), msg.From)
	}
}


func (rf *Raft) handleAppendReply(reply* AppendReply) {
	start := time.Now()
	defer calcRuntime(start, "handleAppendReply")
	fmt.Printf("%d handleAppendReply from %d at %v\n", rf.me, reply.From, start)
	if !rf.checkAppend(reply.From, reply.Term, reply.MsgType) {
		return
	}
	if rf.leader != rf.me || rf.state != Leader{
		return
	}
	pr := &rf.clients[reply.From]
	pr.active = true
	if reply.MsgType == MsgHeartbeatReply {
		if pr.matched < rf.raftLog.GetLastIndex() && pr.PassAppendTimeout() {
			rf.appendMore(reply.From)
		}
		fmt.Printf("%d access HeartbeatReply from %d(matched: %d, %d)\n", rf.me, reply.From,
			pr.matched, rf.raftLog.GetLastIndex())
		return
	} else if reply.MsgType == MsgSnapshotReply {
		fmt.Printf("%d access Snapshot Reply from %d(matched: %d, %d)\n", rf.me, reply.From,
			pr.matched, rf.raftLog.GetLastIndex())
		return
	}
	if !reply.Success {
		fmt.Printf("%d(%d) handleAppendReply failed, from %d(%d). which matched %d\n",
			rf.me, rf.raftLog.commited, reply.From, reply.Commited, pr.matched)
		if reply.Commited + 1 < pr.next {
			pr.next = reply.Commited + 1
			rf.appendMore(reply.From)
		}
	} else {
		fmt.Printf("%d: %d handleAppendReply from %d(%d), commit log from %d to %d\n",
			reply.Id, rf.me, reply.From, reply.Term, pr.matched, reply.Commited)

		if pr.matched < reply.Commited {
			pr.matched = reply.Commited
			pr.next = reply.Commited + 1
		}
/*		if reply.Commited <= rf.raftLog.commited {
			return
		}*/
		commits := make([]int, len(rf.peers))
		for i, p := range rf.clients {
			if i == rf.me {
				commits[i] = rf.raftLog.GetLastIndex()
			} else {
				commits[i] = p.matched
			}
		}
		sort.Ints(commits)
		quorum := len(rf.peers) / 2
		fmt.Printf("%d receive a msg commit : %d from %d\n", rf.me, reply.Commited, reply.From)
		fmt.Printf("%d commit %d, to commit %d, apply %d, all: %d\n",
			rf.me, rf.raftLog.commited, commits[quorum], rf.raftLog.applied,
			rf.raftLog.size)
		if rf.raftLog.commited < commits[quorum] {
			rf.raftLog.commited = commits[quorum]
			for _, e := range rf.raftLog.GetUnApplyEntry() {
				m := rf.createApplyMsg(e)
				if e.Index != rf.raftLog.applied + 1 {
					fmt.Printf("%d APPLY ERROR! %d, %d\n", rf.me, e.Index, rf.raftLog.applied)
				}
				if m.CommandValid {
					rf.applySM <- m
					fmt.Printf("%d apply a message of log[%d]=data[%d]\n", rf.me, e.Index, m.CommandIndex)
				}
				rf.raftLog.applied += 1
			}
			fmt.Printf("%d apply message\n", rf.me)
		}
		fmt.Printf("%d send handleAppendReply end\n", rf.me)
	}
}


func (rf *Raft) checkAppend(from int, term int, msgType MessageType) bool {
	if term > rf.term {
		rf.becomeFollower(term, from)
	} else if term < rf.term {
		fmt.Printf("==================!ERROR!======append message(%s) from %d(%d) to %d(%d) can not be reach, leader: %d\n",
			getMsgName(msgType), from, term, rf.me, rf.term, rf.leader)
		return false
	}
	return true
}
