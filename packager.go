/*
Copyright 2016 The gta AUTHORS. All rights reserved.

Use of this source code is governed by the Apache 2 license that can be found
in the LICENSE file.
*/
package gta

import (
	"fmt"
	"go/build"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

type Package struct {
	ImportPath string

	// Dir the absolute path of the directory containing the package.
	// bug(bc): this is currently unreliable and in GOPATH mode only identifies
	// the src directory for the GOPATH that hosts the package.  Currently, the
	// only guarantee is that Dir will not be empty when the package exists.
	Dir string
}

// graphError is a collection of errors from attempting to build the
// dependent graph.
type graphError struct {
	Errors map[string]error
}

// Error implements the error interface for GraphError.
func (g *graphError) Error() string {
	return fmt.Sprintf("errors while generating import graph: %v", g.Errors)
}

// Packager interface defines a set of means to access golang build Package information.
type Packager interface {
	// Get a go package from directory. Should return a *build.NoGoError value
	// when there are no Go files in the directory.
	PackageFromDir(string) (*Package, error)
	// Get a go package from an empty directory.
	PackageFromEmptyDir(string) (*Package, error)
	// Get a go package from import path. Should return a *build.NoGoError value
	// when there are no Go files in the directory.
	PackageFromImport(string) (*Package, error)
	// DependentGraph returns the DependentGraph for the current
	// Golang workspace as defined by their import paths.
	DependentGraph() (*Graph, error)
}

func NewPackager(prefixes, tags []string) Packager {
	return newPackager(newLoadConfig(tags), prefixes)
}

func newPackager(cfg *packages.Config, prefixes []string) Packager {
	importPathsByDir, g, err := dependencyGraph(cfg, prefixes)
	ctx := build.Default
	return &packageContext{
		ctx:               &ctx,
		err:               err,
		packages:          make(map[string]struct{}),
		reverse:           g,
		modulesNamesByDir: importPathsByDir,
	}
}

// newLoadConfig returns a *packages.Config suitable for use by packages.Load.
// The constructor here is mostly useful for tests.
func newLoadConfig(tags []string) *packages.Config {
	return &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedModule,
		BuildFlags: []string{
			fmt.Sprintf(`-tags=%s`, strings.Join(tags, ",")),
		},
		Tests: true,
	}
}

// packageContext implements the Packager interface.
type packageContext struct {
	ctx *build.Context
	err error
	// packages is a set of import paths of packages that have been imported.
	packages map[string]struct{}
	// reverse is a reverse dependency graph (import path -> (dependent import path -> struct{}{}))
	reverse map[string]map[string]struct{}
	// modulesNamesByDir is a map of directories to import paths. absolute path directory -> import path/module name
	modulesNamesByDir map[string]string

	packagesConfig *packages.Config
}

// PackageFromDir returns a build package from a directory.
func (p *packageContext) PackageFromDir(dir string) (*Package, error) {
	// try importing using ImportDir first so that the expected kinds of errors
	// (e.g. build.NoGoError) will be returned.
	pkg, err := p.ctx.ImportDir(dir, build.ImportComment)
	pkg2 := packageFrom(pkg)
	resolveLocal(pkg2, dir, p.modulesNamesByDir)
	p.packages[pkg2.ImportPath] = struct{}{}
	return pkg2, err
}

// PackageFromEmptyDir returns a build package from a directory.
func (p *packageContext) PackageFromEmptyDir(dir string) (*Package, error) {
	// TODO(bc): construct the Package from the information about the module or GOPATH
	pkg, err := p.ctx.ImportDir(dir, build.FindOnly)
	pkg2 := packageFrom(pkg)
	resolveLocal(pkg2, dir, p.modulesNamesByDir)
	p.packages[pkg2.ImportPath] = struct{}{}
	return pkg2, err
}

// PackageFromImport returns a build package from an import path.
func (p *packageContext) PackageFromImport(importPath string) (*Package, error) {
	pkg, err := p.ctx.Import(importPath, ".", build.ImportComment)
	pkg2 := packageFrom(pkg)
	p.packages[pkg2.ImportPath] = struct{}{}
	return pkg2, err
}

// DependentGraph returns a dependent graph based on the current imported packages.
func (p *packageContext) DependentGraph() (*Graph, error) {
	if p.err != nil {
		return nil, p.err
	}

	graph := make(map[string]map[string]bool)
	for k := range p.reverse {
		inner := make(map[string]bool)
		for k2 := range p.reverse[k] {
			inner[k2] = true
		}
		graph[k] = inner
	}

	return &Graph{graph: graph}, nil
}

func packageFrom(pkg *build.Package) *Package {
	return &Package{
		ImportPath: pkg.ImportPath,
		Dir:        pkg.SrcRoot,
	}
}

// resolveLocal resolves pkg.ImportPath and pkg.SrcRoot for dir against
// importPathsByDir when pkg.ImportPath is a relative path.
func resolveLocal(pkg *Package, dir string, modulesByDir map[string]string) {
	if pkg.ImportPath != "." {
		return
	}

	importPath := pkg.ImportPath

	var mruPrefix string
	for k, v := range modulesByDir {
		// check for an exact match
		if dir == k {
			importPath = v
			break
		}

		// there may be nested modules; make sure the directory being checked is
		// within the directory for current entry and deeper than the most recently
		// matched prefix.
		if !strings.HasPrefix(dir, k) || len(mruPrefix) > len(k) {
			continue
		}

		mruPrefix = k

		vendorPathSegment := "/vendor/"
		candidateImportPath := strings.ReplaceAll(strings.TrimPrefix(dir, k), string(filepath.Separator), "/")

		// vendored packages within modules should not have a `vendor` prefix and
		// will not have one in the value returned from packages.Load, so strip
		// it out.
		if strings.HasPrefix(candidateImportPath, vendorPathSegment) {
			importPath = strings.TrimPrefix(candidateImportPath, vendorPathSegment)
		} else {
			importPath = path.Join(v, candidateImportPath)
		}
	}

	pkg.ImportPath = importPath
}

// dependencyGraph constructs a map of directories to import paths when in
// module aware mode and flattened reverse transitive dependency graph. When in
// GOPATH mode the map of directories to import paths will be empty.
func dependencyGraph(cfg *packages.Config, includePkgs []string) (map[string]string, map[string]map[string]struct{}, error) {
	pkgs := make([]string, 0, len(includePkgs))
	for _, pkg := range includePkgs {
		pkgs = append(pkgs, fmt.Sprintf("%s...", pkg))
	}

	loadedPackages, err := packages.Load(cfg, pkgs...)
	if err != nil {
		return nil, nil, fmt.Errorf("loading packages: %w", err)
	}

	reverse := make(map[string]map[string]struct{})
	importPathsByDir := make(map[string]string)

	seen := make(map[string]struct{})
	var addPackage func(pkg *packages.Package)
	addPackage = func(pkg *packages.Package) {
		if _, ok := seen[pkg.ID]; ok {
			return
		}

		if pkg.Module != nil && pkg.Module.Main {
			importPathsByDir[pkg.Module.Dir] = pkg.Module.Path
		}

		seen[pkg.ID] = struct{}{}

		// Ignore packages that do not have any Go files that satisfy the build
		// constraints.
		if len(pkg.GoFiles) == 0 {
			return
		}

		// Ignore the test binary packages
		if filepath.Ext(pkg.GoFiles[0]) != ".go" && strings.HasSuffix(pkg.PkgPath, ".test") {
			return
		}

		// normalize the import path so that test packages will be flattened into
		// the package path of the primary package.
		pkgPath := normalizeImportPath(pkg)

		for _, importedPkg := range pkg.Imports {
			addPackage(importedPkg)

			importedPath := normalizeImportPath(importedPkg)

			// do not attempt to add the normalized import path to the dependent
			// graph when the normalized import path is the same as the package whose
			// dependents are being calculated.
			if importedPath == pkgPath {
				continue
			}

			if _, ok := reverse[importedPath]; !ok {
				reverse[importedPath] = make(map[string]struct{})
			}
			m := reverse[importedPath]
			m[pkgPath] = struct{}{}
		}
	}

	for _, pkg := range loadedPackages {
		addPackage(pkg)
	}

	return importPathsByDir, reverse, nil
}

// normalizeImportPath will return the import path of pkg. The import path may
// not be pkg.PkgPath (e.g. when pkg is a package for external tests, the final
// segment of pkg.PkgPath will differ from the import path of the package in
// pkg's directory).
func normalizeImportPath(pkg *packages.Package) string {
	files := pkg.GoFiles

	importPath := pkg.PkgPath
	if len(files) == 0 || !(strings.HasSuffix(importPath, "_test") || strings.HasSuffix(importPath, ".test")) {
		return importPath
	}

	ext := filepath.Ext(files[0])
	if ext != ".go" {
		return importPath
	}

	dirBase := filepath.Base(filepath.Dir(files[0]))
	importPathBase := path.Base(importPath)
	if importPathBase != dirBase {
		importPath = path.Join(path.Dir(importPath), dirBase)
	}
	return importPath
}
