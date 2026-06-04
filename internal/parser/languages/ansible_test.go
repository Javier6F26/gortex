package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// metaString reads a string-valued Meta key, returning "" when absent.
func metaString(n *graph.Node, key string) string {
	if n == nil || n.Meta == nil {
		return ""
	}
	if v, ok := n.Meta[key].(string); ok {
		return v
	}
	return ""
}

func TestAnsible_PlaybookEmitsPlayTasksHandlerAndModuleCalls(t *testing.T) {
	src := []byte(`- name: Configure web servers
  hosts: web
  tasks:
    - name: Copy nginx config
      ansible.builtin.copy:
        src: nginx.conf
        dest: /etc/nginx/nginx.conf
      notify: restart nginx
    - name: Ensure nginx running
      service:
        name: nginx
        state: started
  handlers:
    - name: restart nginx
      service:
        name: nginx
        state: restarted
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("playbooks/web.yml", src)
	require.NoError(t, err)

	nodes := nodesByID(result.Nodes)

	// Play node.
	playID := "playbooks/web.yml::play:Configure web servers"
	require.Contains(t, nodes, playID)
	assert.Equal(t, graph.KindType, nodes[playID].Kind)
	assert.Equal(t, "play", metaString(nodes[playID], "ansible_kind"))
	assert.Equal(t, "web", metaString(nodes[playID], "hosts"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDefines, "playbooks/web.yml", playID),
		"file should define the play")

	// Task 1: copy with module meta + module call edge + notify.
	copyID := "playbooks/web.yml::task:Copy nginx config"
	require.Contains(t, nodes, copyID)
	assert.Equal(t, graph.KindFunction, nodes[copyID].Kind)
	assert.Equal(t, "task", metaString(nodes[copyID], "ansible_kind"))
	assert.Equal(t, "ansible.builtin.copy", metaString(nodes[copyID], "module"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDefines, playID, copyID),
		"play should define the copy task")
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeCalls, copyID,
		"unresolved::ansible_module::ansible.builtin.copy"),
		"copy task should call its module")
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeReferences, copyID,
		"unresolved::ansible_handler::restart nginx"),
		"copy task should notify the handler")

	// Task 2: service module.
	svcID := "playbooks/web.yml::task:Ensure nginx running"
	require.Contains(t, nodes, svcID)
	assert.Equal(t, "service", metaString(nodes[svcID], "module"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeCalls, svcID,
		"unresolved::ansible_module::service"),
		"service task should call its module")

	// Handler node.
	handlerID := "playbooks/web.yml::handler:restart nginx"
	require.Contains(t, nodes, handlerID)
	assert.Equal(t, graph.KindFunction, nodes[handlerID].Kind)
	assert.Equal(t, "handler", metaString(nodes[handlerID], "ansible_kind"))
	assert.Equal(t, "service", metaString(nodes[handlerID], "module"))
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDefines, playID, handlerID),
		"play should define the handler")
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeCalls, handlerID,
		"unresolved::ansible_module::service"),
		"handler should call its module")
}

func TestAnsible_PlayRolesEmitReferences(t *testing.T) {
	src := []byte(`- hosts: db
  roles:
    - common
    - role: postgres
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("playbooks/db.yml", src)
	require.NoError(t, err)

	playID := "playbooks/db.yml::play:db"
	nodes := nodesByID(result.Nodes)
	require.Contains(t, nodes, playID)
	assert.Equal(t, "db", metaString(nodes[playID], "hosts"))

	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeReferences, playID,
		"unresolved::ansible_role::common"),
		"play should reference the common role (scalar form)")
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeReferences, playID,
		"unresolved::ansible_role::postgres"),
		"play should reference the postgres role ({role: ...} form)")
}

func TestAnsible_StandaloneTasksFile(t *testing.T) {
	src := []byte(`- name: Install package
  ansible.builtin.apt:
    name: curl
    state: present
- name: Start service
  service:
    name: curl
    state: started
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("roles/web/tasks/main.yml", src)
	require.NoError(t, err)

	nodes := nodesByID(result.Nodes)
	taskID := "roles/web/tasks/main.yml::task:Install package"
	require.Contains(t, nodes, taskID)
	assert.Equal(t, graph.KindFunction, nodes[taskID].Kind)
	assert.Equal(t, "ansible.builtin.apt", metaString(nodes[taskID], "module"))
	// File owns the task directly in a standalone tasks file.
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeDefines, "roles/web/tasks/main.yml", taskID),
		"file should define the standalone task")
	assert.True(t, hasEdgeBetween(result.Edges, graph.EdgeCalls, taskID,
		"unresolved::ansible_module::ansible.builtin.apt"),
		"standalone task should call its module")
}

// A plain YAML list of config objects (no hosts, not under a role tree)
// must NOT be classified as Ansible — it falls through to the generic
// top-level-keys walker, which emits no play/task nodes.
func TestAnsible_PlainListNotClassified(t *testing.T) {
	src := []byte(`- name: us-east
  region: us-east-1
  capacity: 10
- name: eu-west
  region: eu-west-1
  capacity: 5
`)
	// Direct detector check: returns false (not Ansible).
	var negative parser.ExtractionResult
	classified := extractAnsibleYAML("config/regions.yml", "config/regions.yml", src, &negative)
	assert.False(t, classified, "a plain config list must not be classified as Ansible")

	// And via the full extractor: no play / task / handler nodes appear.
	e := NewYAMLExtractor()
	result, err := e.Extract("config/regions.yml", src)
	require.NoError(t, err)
	for _, n := range result.Nodes {
		if n.Meta != nil {
			_, isAnsible := n.Meta["ansible_kind"]
			assert.False(t, isAnsible, "no node should carry ansible_kind meta: %s", n.ID)
		}
	}
}
