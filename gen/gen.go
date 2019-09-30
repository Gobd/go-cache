package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os"
	"os/exec"
	"reflect"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

// This file taken from https://github.com/a8m/syncmap & modified to support go-cache

var (
	out   = flag.String("o", "", "")
	pkg   = flag.String("pkg", "main", "")
	name  = flag.String("name", "", "")
	usage = `Usage: go run ./gen/gen.go [options...] cacheType

Options:
  -o         Specify file output. If none is specified, the name
             will be derived from the map type.
  -pkg       Package name to use in the generated code. If none is
             specified, the name will be main.
  -name      Name of the struct that will be cached. Can almost always
             match the name of the cached item. Cases where a map is cached
             is a good example of when this must be different.
`
)

func main() {
	flag.Usage = func() {
		fmt.Println(usage)
	}
	flag.Parse()
	g, err := NewGenerator()
	failOnErr(err)
	err = g.Mutate()
	failOnErr(err)
	err = g.Gen()
	failOnErr(err)
}

// Generator generates the typed cache object.
type Generator struct {
	// flag options.
	pkg  string // package name.
	out  string // file name.
	key  string // cache type.
	name string // struct name
	// mutation state and traversal handlers.
	file  *ast.File
	fset  *token.FileSet
	funcs map[string]func(*ast.FuncDecl)
	types map[string]func(*ast.TypeSpec)
}

// NewGenerator returns a new generator
func NewGenerator() (g *Generator, err error) {
	defer catch(&err)
	g = &Generator{
		fset: token.NewFileSet(),
		pkg:  *pkg,
		out:  *out,
		name: *name,
		key:  os.Args[len(os.Args)-1],
	}
	g.funcs = g.Funcs()
	g.types = g.Types()
	_, err = parser.ParseExpr(g.key)
	check(err, "parse expr: %s", g.key)
	if g.name == "" {
		panic("name must not be empty")
	}
	if g.out == "" {
		g.out = strings.ToLower(g.name) + ".go"
	}
	return
}

// Mutate mutates the original AST and brings it to the desired state.
// It fails if it encounters an unrecognized node in the AST.
func (g *Generator) Mutate() (err error) {
	defer catch(&err)

	dir, err := os.Getwd()
	if err != nil {
		check(err, "getting filepath")
	}
	path := dir + "/cache.go"

	b, err := ioutil.ReadFile(path)
	check(err, "read %q file", path)
	f, err := parser.ParseFile(g.fset, "", b, parser.ParseComments)
	check(err, "parse %q file", path)
	astutil.AddImport(g.fset, f, "fmt")
	astutil.AddImport(g.fset, f, "runtime")
	astutil.AddImport(g.fset, f, "sync")
	astutil.AddImport(g.fset, f, "time")
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			handler, ok := g.funcs[d.Name.Name]
			expect(ok, "unrecognized function: %s", d.Name.Name)
			handler(d)
			delete(g.funcs, d.Name.Name)
		case *ast.GenDecl:
			switch d := d.Specs[0].(type) {
			case *ast.TypeSpec:
				handler, ok := g.types[d.Name.Name]
				expect(ok, "unrecognized type: %s", d.Name.Name)
				handler(d)
				delete(g.types, d.Name.Name)

			}
		default:
			expect(false, "unrecognized type: %s", d)
		}
	}
	expect(len(g.funcs) == 0, "function was deleted")
	expect(len(g.types) == 0, "type was deleted")
	rename(f, map[string]string{
		"Cache":               strings.Title(g.name) + "Cache",
		"item":                strings.ToLower(g.name) + "Item",
		"cache":               strings.ToLower(g.name) + "Cache",
		"lazyTime":            strings.ToLower(g.name) + "LazyTime",
		"stopJanitor":         strings.ToLower(g.name) + "StopJanitor",
		"runJanitor":          strings.ToLower(g.name) + "RunJanitor",
		"newCache":            strings.ToLower(g.name) + "NewCache",
		"newCacheWithJanitor": strings.ToLower(g.name) + "NewCacheWithJanitor",
		"New":                 "New" + strings.Title(g.name),
		"NewLazy":             "NewLazy" + strings.Title(g.name),
		"janitor":             strings.ToLower(g.name) + "Janitor",
	})
	f.Name.Name = g.pkg
	g.file = f
	return
}

// Gen dumps the mutated AST to a file in the configured destination.
func (g *Generator) Gen() (err error) {
	defer catch(&err)
	b := bytes.NewBuffer([]byte("// Code generated; DO NOT EDIT.\n\n"))
	err = format.Node(b, g.fset, g.file)
	check(err, "format mutated code")
	err = ioutil.WriteFile(g.out, b.Bytes(), 0644)
	check(err, "writing file: %s", g.out)
	err = exec.Command("goimports", "-w", g.out).Run()
	check(err, "running goimports on: %s", g.out)
	return
}

// Types returns all TypesSpec handlers for AST mutation.
func (g *Generator) Types() map[string]func(*ast.TypeSpec) {
	return map[string]func(*ast.TypeSpec){
		"item": func(t *ast.TypeSpec) {
			g.replaceItem(t.Type)
		},
		"lockedMap":    func(t *ast.TypeSpec) {},
		"stringStruct": func(t *ast.TypeSpec) {},
		"Cache":        func(t *ast.TypeSpec) {},
		"cache":        func(t *ast.TypeSpec) {},
		"lazyTime":     func(*ast.TypeSpec) {},
		"janitor":      func(*ast.TypeSpec) {},
	}
}

// Funcs returns all FuncDecl handlers for AST mutation.
func (g *Generator) Funcs() map[string]func(*ast.FuncDecl) {
	nop := func(*ast.FuncDecl) {}
	return map[string]func(*ast.FuncDecl){
		"Expired": nop,
		"now":     nop,
		"Set": func(f *ast.FuncDecl) {
			g.replaceItem(f.Type.Params)
		},
		"set": func(f *ast.FuncDecl) {
			g.replaceItem(f.Type.Params)
		},
		"SetDefault": func(f *ast.FuncDecl) {
			g.replaceItem(f.Type.Params)
		},
		"Add": func(f *ast.FuncDecl) {
			g.replaceItem(f.Type.Params)
		},
		"Get": func(f *ast.FuncDecl) {
			g.replaceItem(f.Type.Results)
		},
		"GetWithExpiration": func(f *ast.FuncDecl) {
			g.replaceItem(f.Type.Results)
		},
		"get": func(f *ast.FuncDecl) {
			g.replaceItem(f.Type.Results)
		},
		"Delete":              nop,
		"DeleteExpired":       nop,
		"ItemCount":           nop,
		"Flush":               nop,
		"Run":                 nop,
		"stopJanitor":         nop,
		"runJanitor":          nop,
		"newCache":            nop,
		"newCacheWithJanitor": nop,
		"New":                 nop,
		"NewLazy":             nop,
		"memhash":             nop,
		"memHash":             nop,
		"memHashString":       nop,
		"keyToHash":           nop,
	}
}

// replaceItem replaces all `interface{}` occurrences in the given Node with the key node.
func (g *Generator) replaceItem(n ast.Node) { replaceIface(n, g.key) }

func replaceIface(n ast.Node, s string) {
	var skip bool // Used to skip replacing `k` interface{} with string key will remain interface{}
	astutil.Apply(n, func(c *astutil.Cursor) bool {
		n := c.Node()
		if v, ok := n.(*ast.Field); ok {
			if v.Names[0].Name == "k" {
				skip = true
			}
		}

		if it, ok := n.(*ast.InterfaceType); ok {
			if skip {
				skip = false
				return true
			}
			c.Replace(expr(s, it.Interface))
		}
		return true
	}, nil)
}

func rename(f *ast.File, oldnew map[string]string) {
	astutil.Apply(f, func(c *astutil.Cursor) bool {
		switch n := c.Node().(type) {
		case *ast.Ident:
			if name, ok := oldnew[n.Name]; ok {
				n.Name = name
			}
		case *ast.FuncDecl:
			if name, ok := oldnew[n.Name.Name]; ok {
				n.Name.Name = name
			}
		default:
		}
		return true
	}, nil)
}

func expr(s string, pos token.Pos) ast.Expr {
	exp, err := parser.ParseExpr(s)
	check(err, "parse expr: %q", s)
	setPos(exp, pos)
	return exp
}

func setPos(n ast.Node, p token.Pos) {
	if reflect.ValueOf(n).IsNil() {
		return
	}
	switch n := n.(type) {
	case *ast.Ident:
		n.NamePos = p
	case *ast.MapType:
		n.Map = p
		setPos(n.Key, p)
		setPos(n.Value, p)
	case *ast.FieldList:
		n.Closing = p
		n.Opening = p
		if len(n.List) > 0 {
			setPos(n.List[0], p)
		}
	case *ast.Field:
		setPos(n.Type, p)
		if len(n.Names) > 0 {
			setPos(n.Names[0], p)
		}
	case *ast.FuncType:
		n.Func = p
		setPos(n.Params, p)
		setPos(n.Results, p)
	case *ast.ArrayType:
		n.Lbrack = p
		setPos(n.Elt, p)
	case *ast.StructType:
		n.Struct = p
		setPos(n.Fields, p)
	case *ast.SelectorExpr:
		setPos(n.X, p)
		n.Sel.NamePos = p
	case *ast.InterfaceType:
		n.Interface = p
		setPos(n.Methods, p)
	case *ast.StarExpr:
		n.Star = p
		setPos(n.X, p)
	default:
		panic(fmt.Sprintf("unknown type: %v", n))
	}
}

// check panics if the error is not nil.
func check(err error, msg string, args ...interface{}) {
	if err != nil {
		args = append(args, err)
		panic(genError{fmt.Sprintf(msg+": %s", args...)})
	}
}

// expect panic if the condition is false.
func expect(cond bool, msg string, args ...interface{}) {
	if !cond {
		panic(genError{fmt.Sprintf(msg, args...)})
	}
}

type genError struct {
	msg string
}

func (p genError) Error() string { return fmt.Sprintf("cache: %s", p.msg) }

func catch(err *error) {
	if e := recover(); e != nil {
		gerr, ok := e.(genError)
		if !ok {
			panic(e)
		}
		*err = gerr
	}
}

func failOnErr(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n", err.Error())
		os.Exit(1)
	}
}
