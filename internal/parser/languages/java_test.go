package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestJavaExtractor_Class(t *testing.T) {
	src := []byte(`public class UserService {
    public User getUser(String id) {
        return null;
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("UserService.java", src)
	require.NoError(t, err)

	types := nodesOfKind(result.Nodes, graph.KindType)
	require.Len(t, types, 1)
	assert.Equal(t, "UserService", types[0].Name)

	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	require.Len(t, methods, 1)
	assert.Equal(t, "getUser", methods[0].Name)
}

func TestJavaExtractor_Interface(t *testing.T) {
	src := []byte(`public interface Repository {
    User findById(String id);
    void save(User user);
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("Repository.java", src)
	require.NoError(t, err)

	ifaces := nodesOfKind(result.Nodes, graph.KindInterface)
	require.Len(t, ifaces, 1)
	assert.Equal(t, "Repository", ifaces[0].Name)
}

func TestJavaExtractor_Enum(t *testing.T) {
	src := []byte(`public enum Status {
    ACTIVE,
    INACTIVE,
    PENDING;
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("Status.java", src)
	require.NoError(t, err)

	types := nodesOfKind(result.Nodes, graph.KindType)
	require.Len(t, types, 1)
	assert.Equal(t, "Status", types[0].Name)
	require.NotNil(t, types[0].Meta, "enum should carry Meta[\"kind\"]=\"enum\"")
	assert.Equal(t, "enum", types[0].Meta["kind"])

	members := map[string]bool{}
	for _, n := range result.Nodes {
		if n.Kind == graph.KindEnumMember {
			members[n.Name] = true
		}
	}
	assert.Equal(t, map[string]bool{"ACTIVE": true, "INACTIVE": true, "PENDING": true}, members)
}

func TestJavaExtractor_MethodMemberOf(t *testing.T) {
	src := []byte(`public class UserService {
    private String name;

    public UserService(String name) {
        this.name = name;
    }

    public User findUser(String id) {
        return db.query(id);
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("UserService.java", src)
	require.NoError(t, err)

	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	assert.GreaterOrEqual(t, len(methods), 2)

	memberEdges := edgesOfKind(result.Edges, graph.EdgeMemberOf)
	assert.GreaterOrEqual(t, len(memberEdges), 2)
	for _, e := range memberEdges {
		assert.Equal(t, "UserService.java::UserService", e.To)
	}
}

func TestJavaExtractor_FieldMemberOf(t *testing.T) {
	src := []byte(`public class UserService {
    private String name;
    public int count;
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("UserService.java", src)
	require.NoError(t, err)

	fields := nodesOfKind(result.Nodes, graph.KindField)
	assert.Len(t, fields, 2)

	memberEdges := edgesOfKind(result.Edges, graph.EdgeMemberOf)
	assert.Len(t, memberEdges, 2)
	for _, e := range memberEdges {
		assert.Equal(t, "UserService.java::UserService", e.To)
	}
}

func TestJavaExtractor_InterfaceMethods(t *testing.T) {
	src := []byte(`public interface Repository {
    User findById(String id);
    void save(User user);
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("Repository.java", src)
	require.NoError(t, err)

	ifaces := nodesOfKind(result.Nodes, graph.KindInterface)
	require.Len(t, ifaces, 1)
	assert.Equal(t, "Repository", ifaces[0].Name)
	require.NotNil(t, ifaces[0].Meta)
	methods, ok := ifaces[0].Meta["methods"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"findById", "save"}, methods)
}

func TestJavaExtractor_TypeEnv_ExplicitType(t *testing.T) {
	src := []byte(`public class App {
    public void run() {
        UserService svc = new UserService();
        svc.findUser("123");
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("App.java", src)
	require.NoError(t, err)

	// The call to svc.findUser should have receiver_type = "UserService".
	callEdges := edgesOfKind(result.Edges, graph.EdgeCalls)
	var found bool
	for _, edge := range callEdges {
		if edge.To == "unresolved::*.findUser" && edge.Meta != nil {
			rt, ok := edge.Meta["receiver_type"].(string)
			if ok && rt == "UserService" {
				found = true
			}
		}
	}
	assert.True(t, found, "expected call edge with receiver_type=UserService for svc.findUser()")
}

func TestJavaExtractor_TypeEnv_NewExpression(t *testing.T) {
	src := []byte(`public class App {
    public void run() {
        var repo = new UserRepository();
        repo.save(null);
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("App.java", src)
	require.NoError(t, err)

	// The call to repo.save should have receiver_type = "UserRepository" (inferred from new).
	callEdges := edgesOfKind(result.Edges, graph.EdgeCalls)
	var found bool
	for _, edge := range callEdges {
		if edge.To == "unresolved::*.save" && edge.Meta != nil {
			rt, ok := edge.Meta["receiver_type"].(string)
			if ok && rt == "UserRepository" {
				found = true
			}
		}
	}
	assert.True(t, found, "expected call edge with receiver_type=UserRepository for repo.save()")
}

func TestJavaExtractor_TypeEnv_UnknownType(t *testing.T) {
	src := []byte(`public class App {
    public void run() {
        unknown.doSomething();
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("App.java", src)
	require.NoError(t, err)

	// The call to unknown.doSomething should NOT have receiver_type metadata.
	callEdges := edgesOfKind(result.Edges, graph.EdgeCalls)
	for _, edge := range callEdges {
		if edge.To == "unresolved::*.doSomething" {
			if edge.Meta != nil {
				_, hasRecvType := edge.Meta["receiver_type"]
				assert.False(t, hasRecvType, "expected no receiver_type for unknown receiver")
			}
		}
	}
}

func TestJavaExtractor_TypeEnv_Chain(t *testing.T) {
	src := []byte(`public class App {
    public void run() {
        Connection conn = new Connection();
        conn.query().first().save();
    }
}

class Connection {
    public Result query() {
        return new Result();
    }
}

class Result {
    public User first() {
        return new User();
    }
}

class User {
    public void save() {}
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("App.java", src)
	require.NoError(t, err)

	callEdges := edgesOfKind(result.Edges, graph.EdgeCalls)
	var found bool
	for _, edge := range callEdges {
		if edge.To == "unresolved::*.save" && edge.Meta != nil {
			rt, ok := edge.Meta["receiver_type"].(string)
			if ok && rt == "User" {
				found = true
			}
		}
	}
	assert.True(t, found, "expected call edge with receiver_type=User for chained conn.query().first().save()")
}

func TestJavaExtractor_Imports(t *testing.T) {
	src := []byte(`import java.util.List;
import com.example.service.UserService;

public class App {}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("App.java", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	require.Len(t, imports, 2)
}

func TestJavaExtractor_SpringBeanAnnotation(t *testing.T) {
	// @Bean on a method inside a @Configuration class should emit an
	// EdgeProvides from the class to the method with binding="bean"
	// and provides_for set to the return type. Without this the
	// indexer's DI post-pass can't link bean consumers back to the
	// factory.
	src := []byte(`
package com.example;

import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import java.time.Clock;

@Configuration
public class Clocks {
    @Bean
    public Clock systemClock() {
        return Clock.systemUTC();
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("Clocks.java", src)
	require.NoError(t, err)

	var found *graph.Edge
	for _, ed := range edgesOfKind(result.Edges, graph.EdgeProvides) {
		if ed.Meta == nil {
			continue
		}
		if b, _ := ed.Meta["binding"].(string); b == "bean" {
			found = ed
			break
		}
	}
	require.NotNil(t, found, "expected @Bean provides edge")
	assert.Equal(t, "Clock", found.Meta["provides_for"])
	assert.Equal(t, "Clocks.java::Clocks.systemClock", found.To)
}

func TestJavaExtractor_ConstructorParamsCaptured(t *testing.T) {
	// Constructor nodes stash params_src on Meta so the indexer's
	// Spring-bean post-pass can match consumers to factories by
	// type-name presence in the signature.
	src := []byte(`
package c;
public class X {
    private final Clock clock;
    public X(Clock clock, int foo) { this.clock = clock; }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("X.java", src)
	require.NoError(t, err)

	var ctor *graph.Node
	for _, n := range nodesOfKind(result.Nodes, graph.KindMethod) {
		if n.Name == "X.<init>" {
			ctor = n
			break
		}
	}
	require.NotNil(t, ctor)
	params, _ := ctor.Meta["params_src"].(string)
	assert.Contains(t, params, "Clock")
	assert.Contains(t, params, "foo")
}

func TestJavaExtractor_BeanMethodParamsCaptured(t *testing.T) {
	src := []byte(`
package c;
public class X {
    public JdbcTemplate jdbcTemplate(DataSource dataSource) { return null; }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("X.java", src)
	require.NoError(t, err)

	var method *graph.Node
	for _, n := range nodesOfKind(result.Nodes, graph.KindMethod) {
		if n.Name == "jdbcTemplate" {
			method = n
			break
		}
	}
	require.NotNil(t, method)
	params, _ := method.Meta["params_src"].(string)
	assert.Contains(t, params, "DataSource")
	assert.Contains(t, params, "dataSource")
}

func TestJavaExtractor_SpringConditionalOnPropertyReadsDatasource(t *testing.T) {
	src := []byte(`
package c;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;

@ConditionalOnProperty(prefix = "spring.datasource", name = "url")
public class JdbcConfig {
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("JdbcConfig.java", src)
	require.NoError(t, err)

	var cls *graph.Node
	for _, n := range nodesOfKind(result.Nodes, graph.KindType) {
		if n.Name == "JdbcConfig" {
			cls = n
			break
		}
	}
	require.NotNil(t, cls)
	keys, _ := cls.Meta["spring_config_keys"].([]string)
	assert.Contains(t, keys, "spring.datasource.url")
}

func TestJavaExtractor_SpringConfigurationPropertiesStampsClass(t *testing.T) {
	src := []byte(`
package c;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.validation.annotation.Validated;

@Validated
@ConfigurationProperties(prefix = "app.jdbc")
public class JdbcFeatureProperties {
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("JdbcFeatureProperties.java", src)
	require.NoError(t, err)

	var cls *graph.Node
	var ann *graph.Node
	for _, n := range result.Nodes {
		switch n.ID {
		case "JdbcFeatureProperties.java::JdbcFeatureProperties":
			cls = n
		case "annotation::java::ConfigurationProperties":
			ann = n
		}
	}
	require.NotNil(t, cls)
	keys, _ := cls.Meta["spring_config_keys"].([]string)
	assert.Contains(t, keys, "app.jdbc.*")
	if ann != nil {
		_, stampedAnnotation := ann.Meta["spring_config_keys"]
		assert.False(t, stampedAnnotation, "configuration property reads should be stamped on the annotated class")
	}
}

func TestJavaConditionalOnPropertyKeys(t *testing.T) {
	assert.Equal(t,
		[]string{"spring.datasource.url", "spring.datasource.username"},
		javaConditionalOnPropertyKeys(`prefix = "spring.datasource", name = {"url", "username"}`),
	)
	assert.Equal(t,
		[]string{"spring.datasource.url"},
		javaConditionalOnPropertyKeys(`"spring.datasource.url"`),
	)
}

func TestJavaConfigurationPropertiesPrefix(t *testing.T) {
	assert.Equal(t, "app.jdbc", javaConfigurationPropertiesPrefix(`prefix = "app.jdbc"`))
	assert.Equal(t, "app.jdbc", javaConfigurationPropertiesPrefix(`"app.jdbc"`))
}

func TestJavaExtractor_DocAndVisibility(t *testing.T) {
	src := []byte(`package x;

/**
 * Greeter is the public greeter.
 */
public class Greeter {
    /**
     * Says hi.
     */
    public void hello() {}

    private void secret() {}
}

/** Internal worker. */
class Worker {}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("Greeter.java", src)
	require.NoError(t, err)

	byID := map[string]*graph.Node{}
	for _, n := range result.Nodes {
		byID[n.ID] = n
	}

	greeter := byID["Greeter.java::Greeter"]
	require.NotNil(t, greeter)
	if greeter.Meta["visibility"] != "public" {
		t.Fatalf("Greeter.vis = %q", greeter.Meta["visibility"])
	}
	if greeter.Meta["doc"] != "Greeter is the public greeter." {
		t.Fatalf("Greeter.doc = %q", greeter.Meta["doc"])
	}

	hello := byID["Greeter.java::Greeter.hello"]
	require.NotNil(t, hello)
	if hello.Meta["visibility"] != "public" {
		t.Fatalf("hello.vis = %q", hello.Meta["visibility"])
	}
	if hello.Meta["doc"] != "Says hi." {
		t.Fatalf("hello.doc = %q", hello.Meta["doc"])
	}

	secret := byID["Greeter.java::Greeter.secret"]
	require.NotNil(t, secret)
	if secret.Meta["visibility"] != "private" {
		t.Fatalf("secret.vis = %q", secret.Meta["visibility"])
	}

	worker := byID["Greeter.java::Worker"]
	require.NotNil(t, worker)
	if worker.Meta["visibility"] != "package" {
		t.Fatalf("Worker.vis = %q", worker.Meta["visibility"])
	}
	if worker.Meta["doc"] != "Internal worker." {
		t.Fatalf("Worker.doc = %q", worker.Meta["doc"])
	}
}

func TestJavaExtractor_AnnotationEdges(t *testing.T) {
	src := []byte(`package x;

@Component
@RequestMapping("/api")
public class UserController {
    @GetMapping("/users/{id}")
    @Deprecated
    public User getUser(int id) { return null; }

    @Autowired
    private UserService userService;
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("UserController.java", src)
	require.NoError(t, err)

	annNames := map[string]bool{}
	for _, n := range result.Nodes {
		if v, _ := n.Meta["kind"].(string); v == "annotation" {
			annNames[n.Name] = true
		}
	}
	for _, want := range []string{"Component", "RequestMapping", "GetMapping", "Deprecated"} {
		if !annNames[want] {
			t.Fatalf("missing annotation node %q (got %v)", want, annNames)
		}
	}

	edges := map[string][]string{}
	for _, e := range result.Edges {
		if e.Kind != graph.EdgeAnnotated {
			continue
		}
		edges[e.From] = append(edges[e.From], e.To)
	}

	classID := "UserController.java::UserController"
	if !javaTestContains(edges[classID], "annotation::java::Component") {
		t.Fatalf("missing @Component edge on class, got %v", edges[classID])
	}
	if !javaTestContains(edges[classID], "annotation::java::RequestMapping") {
		t.Fatalf("missing @RequestMapping edge on class, got %v", edges[classID])
	}

	methodID := "UserController.java::UserController.getUser"
	if !javaTestContains(edges[methodID], "annotation::java::GetMapping") {
		t.Fatalf("missing @GetMapping edge on method, got %v", edges[methodID])
	}
	if !javaTestContains(edges[methodID], "annotation::java::Deprecated") {
		t.Fatalf("missing @Deprecated edge on method, got %v", edges[methodID])
	}
}

func javaTestContains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func TestJavaExtractor_ThrowsClause(t *testing.T) {
	src := []byte(`package x;

public class Reader {
    public String read() throws IOException, ParseException {
        return "";
    }

    public void noThrows() {}
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("Reader.java", src)
	require.NoError(t, err)

	throws := edgesOfKind(result.Edges, graph.EdgeThrows)
	throwTargets := map[string]bool{}
	for _, e := range throws {
		if e.From == "Reader.java::Reader.read" {
			throwTargets[e.To] = true
		}
	}
	assert.True(t, throwTargets["unresolved::IOException"], "IOException not surfaced")
	assert.True(t, throwTargets["unresolved::ParseException"], "ParseException not surfaced")

	for _, e := range throws {
		if e.From == "Reader.java::Reader.noThrows" {
			t.Fatalf("noThrows shouldn't have EdgeThrows, got %v", e)
		}
	}
}

// TestJavaConstClassificationAndPackageScope is part of the C9 set: `static
// final` fields classify as constants, enum members become enum-member nodes,
// and every type/member carries its package scope.
func TestJavaConstClassificationAndPackageScope(t *testing.T) {
	src := []byte("package com.app.core;\n" +
		"public class C {\n" +
		"  public static final int MAX = 10;\n" +
		"  private int y;\n" +
		"}\n" +
		"enum E { A, B }\n")
	res, err := NewJavaExtractor().Extract("C.java", src)
	require.NoError(t, err)
	byName := map[string]*graph.Node{}
	for _, n := range res.Nodes {
		byName[n.Name] = n
	}
	require.NotNil(t, byName["MAX"])
	assert.Equal(t, graph.KindConstant, byName["MAX"].Kind, "static final → constant")
	assert.Equal(t, graph.KindField, byName["y"].Kind)
	assert.Equal(t, "com.app.core", byName["MAX"].Meta["scope_pkg"])
	assert.Equal(t, "com.app.core", byName["C"].Meta["scope_pkg"])
	require.NotNil(t, byName["A"])
	assert.Equal(t, graph.KindEnumMember, byName["A"].Kind, "enum member node")
}
