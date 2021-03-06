package worker

import (
	"github.com/dgraph-io/dgraph/conn"
	"github.com/dgraph-io/dgraph/posting"
	"github.com/dgraph-io/dgraph/task"
	"github.com/dgraph-io/dgraph/x"
	"github.com/dgryski/go-farm"
	"github.com/google/flatbuffers/go"
)

func ProcessTaskOverNetwork(qu []byte) (result []byte, rerr error) {
	uo := flatbuffers.GetUOffsetT(qu)
	q := new(task.Query)
	q.Init(qu, uo)

	attr := string(q.Attr())
	idx := farm.Fingerprint64([]byte(attr)) % numInstances
	glog.WithField("idx", idx).WithField("attr", attr).
		WithField("numInstances", numInstances).Debug("ProcessTaskOverNetwork")

	var runHere bool
	if attr == "_xid_" || attr == "_uid_" {
		idx = 0
		runHere = (instanceIdx == 0)
	} else {
		runHere = (instanceIdx == idx)
	}

	if runHere {
		// No need for a network call, as this should be run from within
		// this instance.
		return processTask(qu)
	}

	pool := pools[idx]
	addr := pool.Addr
	query := new(conn.Query)
	query.Data = qu
	reply := new(conn.Reply)
	if err := pool.Call("Worker.ServeTask", query, reply); err != nil {
		glog.WithField("call", "Worker.ServeTask").Fatal(err)
	}
	glog.WithField("reply_len", len(reply.Data)).WithField("addr", addr).
		Debug("Got reply from server")
	return reply.Data, nil
}

func processTask(query []byte) (result []byte, rerr error) {
	uo := flatbuffers.GetUOffsetT(query)
	q := new(task.Query)
	q.Init(query, uo)
	attr := string(q.Attr())

	b := flatbuffers.NewBuilder(0)
	voffsets := make([]flatbuffers.UOffsetT, q.UidsLength())
	uoffsets := make([]flatbuffers.UOffsetT, q.UidsLength())

	for i := 0; i < q.UidsLength(); i++ {
		uid := q.Uids(i)
		key := posting.Key(uid, attr)
		pl := posting.GetOrCreate(key, dataStore)

		var valoffset flatbuffers.UOffsetT
		if val, err := pl.Value(); err != nil {
			valoffset = b.CreateByteVector(x.Nilbyte)
		} else {
			valoffset = b.CreateByteVector(val)
		}
		task.ValueStart(b)
		task.ValueAddVal(b, valoffset)
		voffsets[i] = task.ValueEnd(b)

		ulist := pl.GetUids()
		uoffsets[i] = x.UidlistOffset(b, ulist)
	}
	task.ResultStartValuesVector(b, len(voffsets))
	for i := len(voffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(voffsets[i])
	}
	valuesVent := b.EndVector(len(voffsets))

	task.ResultStartUidmatrixVector(b, len(uoffsets))
	for i := len(uoffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(uoffsets[i])
	}
	matrixVent := b.EndVector(len(uoffsets))

	task.ResultStart(b)
	task.ResultAddValues(b, valuesVent)
	task.ResultAddUidmatrix(b, matrixVent)
	rend := task.ResultEnd(b)
	b.Finish(rend)
	return b.Bytes[b.Head():], nil
}
