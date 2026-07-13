package agent

import (
	"sync"
	"testing"
)

func TestTreePlannerCallbackPanicsAreContained(t *testing.T) {
	tp := NewTreePlanner(nil, nil, nil, nil)
	tp.SetCallbacks(
		func(*PlanTree, *PlanNode) { panic("start") },
		func(*PlanTree, *PlanNode, bool) { panic("complete") },
		func(*PlanTree, *ReplanContext) { panic("replan") },
		func(*PlannedAction) { panic("progress") },
	)
	node := &PlanNode{ID: "node-1"}
	tree := &PlanTree{}
	tp.invokeNodeStart(tree, node)
	tp.invokeNodeComplete(tree, node, true)
	tp.invokeReplan(tree, &ReplanContext{FailedNode: node})
	tp.invokeProgress(&PlannedAction{})
}

func TestTreePlannerCallbacksCanBeReconfiguredConcurrently(t *testing.T) {
	tp := NewTreePlanner(nil, nil, nil, nil)
	node := &PlanNode{ID: "node-1"}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			tp.SetCallbacks(func(*PlanTree, *PlanNode) {}, nil, nil, nil)
		}()
		go func() {
			defer wg.Done()
			tp.invokeNodeStart(&PlanTree{}, node)
		}()
	}
	wg.Wait()
}
