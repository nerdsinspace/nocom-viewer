/*
 * Copyright (C) 2021 Nerds Inc and/or its affiliates. All rights reserved.
 */

package main

import (
	"bufio"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/dim13/colormap"

	echo "github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	nether := makeTrees("/Users/leijurv/Downloads/heatmap_nether.csv", 250000/16/8, 12)
	overworld := makeTrees("/Users/leijurv/Downloads/heatmap_full.csv", 250000/16, 15)
	server(overworld, nether)
}

func makeTrees(path string, limit int64, denseSize int) Trees {
	hits := load(path)
	log.Println("Loaded", len(hits), "hits")
	// TODO denseSpawn height optimal value will be different in nether vs overworld
	denseSpawn := createDense(denseSize)   // just a RAM tradeoff, might be wrong, optimal value could be plus or minus 1 or 2 from this idk golang is weird
	sparseTotal := createSparse(23) // 2^(23-1) chunks is the width of the world (67 million blocks)
	denseWidth := denseSpawn.width
	sparseWidth := sparseTotal.width
	log.Println("Dense width", denseWidth)
	log.Println("Sparse width", sparseWidth)
	denseOffset := denseWidth / 2
	sparseOffset := sparseWidth / 2
	totalHitsInDense := 0
	totalHits := 0
	for _, hit := range hits {
		//if hit.x < -limit || hit.x >= limit || hit.z < -limit || hit.z >= limit {
		if hit.distSq() > limit*limit {
			continue
		}
		x := hit.x + denseOffset
		z := hit.z + denseOffset
		if x >= 0 && x < denseWidth && z >= 0 && z < denseWidth {
			totalHitsInDense += hit.cnt
			denseSpawn.walkAndIncrement(x, z, hit.cnt)
		} else {
			x = hit.x + sparseOffset
			z = hit.z + sparseOffset
			if x >= 0 && x < sparseWidth && z >= 0 && z < sparseWidth {
				totalHits += hit.cnt
				sparseTotal.walkAndIncrement(x, z, hit.cnt)
			} else {
				panic("hit too far")
			}
		}
	}
	log.Println("Sparse quadtree backing array length is", len(sparseTotal.nodes), "and each one should take up 20 bytes of RAM")
	log.Println("Dense quadtree backing array length is", len(denseSpawn.tree), "and each one should take up 4 bytes of RAM")
	if totalHitsInDense != int(denseSpawn.tree[0]) {
		panic("dense overflow")
	}
	if totalHits != int(sparseTotal.nodes[sparseTotal.root].hits) {
		panic("sparse overflow")
	}
	return Trees{denseSpawn, sparseTotal}
}

func server(overworld Trees, nether Trees) {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			if strings.HasSuffix(c.Request().URL.Path, ".png") {
				parts := strings.Split(c.Request().URL.Path, "tl/")
				if len(parts) > 1 {
					p1 := parts[1]
					if path, ok := parsePath(p1); ok {
						log.Println("Need to do path", path)
						tree := overworld
						if strings.Contains(c.Request().URL.Path, "nether") {
							tree = nether
						}
						blackAndWhite := strings.Contains(c.Request().URL.Path, "blackwhite")
						data := render(path, tree, 9, blackAndWhite) // 256 because it's -1
						c.Response().Header().Set("Content-Type", "image/png")
						c.Response().WriteHeader(http.StatusOK)
						return png.Encode(c.Response(), data)
					}
				}
			}
			return next(c)
		}
	})
	e.Static("/", "static")
	e.Logger.Fatal(e.Start(":4679"))
}

type Trees struct {
	dense DenseQuadtree
	sparse SparseQuadtree
}

// input: 1/2/3.png
// output: [1, 2, 3], true
// input: base.png
// output: [], true
// input: blah blah invalid
// output: whatever, false
func parsePath(path string) ([]int, bool) {
	if !strings.HasSuffix(path, ".png") {
		return nil, false
	}
	path = path[:len(path)-4]
	if path == "base" {
		return []int{}, true
	}
	paths := strings.Split(path, "/")
	ret := make([]int, len(paths))
	for i, path := range paths {
		val, err := strconv.Atoi(path)
		if err != nil || val < 1 || val > 4 {
			return nil, false
		}
		ret[i] = val
	}
	return ret, true
}

func hitCntToHeat(cnt Hits, scalePow int64) uint8 {
	if cnt == 0 {
		return 255
	}
	val := math.Log(float64(cnt) / float64(scalePow)) // this is log base e, NOT log base 10, NOT log base 2
	if val < 0 {
		val = 0
	}
	val *= 50
	val += 50
	if val > 255 {
		val = 255
	}
	gray := uint8(255 - int(val))
	return gray
}

func heatToColor(v uint8, blackAndWhite bool) color.RGBA {
	if blackAndWhite {
		return color.RGBA{v, v, v, 255} // alpha 255 = opaque
	} else {
		return colormap.Magma[255-v].(color.RGBA)
	}
}

func render(path []int, trees Trees, levelSz int, blackAndWhite bool) *image.RGBA {
	dense :=trees.dense
	sparse := trees.sparse
	tooBigBy := len(path) + levelSz - sparse.levels
	//log.Println("Raw tbb", tooBigBy)
	extraNodeLevelsPerPixel := 0
	if tooBigBy < 0 {
		extraNodeLevelsPerPixel = -tooBigBy
		tooBigBy = 0 // 1 node is SMALLER THAN one pixel
	} // if equal, one node EQUALS one pixel
	imgSz := 1
	for i := 1; i < levelSz; i++ {
		imgSz *= 2
	}
	var scaleAmt int64 = 1
	for i := 0; i < extraNodeLevelsPerPixel; i++ {
		scaleAmt *= 4
	}
	log.Println("Scale is", scaleAmt)
	img := image.NewRGBA(image.Rectangle{image.Point{0, 0}, image.Point{imgSz, imgSz}})

	locationToDraw := HybridNode{}
	for _, item := range path {
		item -= 1 // leaflet uses 1234 but we prefer 0123
		locationToDraw = traverse(locationToDraw, item, sparse)
	}

	for y := 0; y < imgSz; y++ {
		for x := 0; x < imgSz; x++ {
			pos := locationToDraw
			for i := levelSz - 2; i >= tooBigBy; i-- {
				pos = traverse(pos, ((x>>i)&1)+((y>>i)&1)<<1, sparse)
			}
			c := uint8(255)
			if !pos.miss {
				c = hitCntToHeat(getHit(pos, sparse, dense), scaleAmt)
			}
			img.SetRGBA(x, y, heatToColor(c, blackAndWhite))
		}
	}
	return img
}

func (data HitData) distSq() int64 {
	return int64(data.x)*int64(data.x)+int64(data.z)*int64(data.z)
}

type HitData struct {
	x   int
	z   int
	cnt int
}

type NodePtr int32
type QuadtreeNode struct {
	Children [4]NodePtr
	hits     Hits
}

type SparseQuadtree struct {
	root   NodePtr
	nodes  []QuadtreeNode
	levels int
	width  int
}

func createSparse(levels int) SparseQuadtree {
	if levels < 1 {
		panic("no")
	}
	width := 1
	for i := 1; i < levels; i++ {
		width *= 2
	}
	ret := SparseQuadtree{levels: levels, nodes: make([]QuadtreeNode, 0), width: width}
	ret.root = ret.alloc()
	return ret
}

func (tree *SparseQuadtree) walkAndIncrement(x int, y int, cnt int) {
	node := &tree.nodes[tree.root]
	node.applyHits(cnt)
	for i := tree.levels - 2; i >= 0; i-- {
		ind := ((x >> i) & 1) + ((y>>i)&1)<<1
		if node.Children[ind] == -1 {
			node.Children[ind] = tree.alloc()
		}
		node = &tree.nodes[node.Children[ind]]
		node.applyHits(cnt)
	}
}

func (node *QuadtreeNode) applyHits(cnt int) {
	orig := node.hits
	sum := orig + Hits(cnt)
	if sum < orig {
		panic("overflow")
	}
	node.hits = sum
}

func (tree *SparseQuadtree) alloc() NodePtr {
	ret := NodePtr(len(tree.nodes))
	tree.nodes = append(tree.nodes, QuadtreeNode{Children: [4]NodePtr{-1, -1, -1, -1}})
	return ret
}

type PackedSparseQuadtreeEntry struct {
	cumulativeQuantityOfDescendents int
	grayscale uint8 // computed as above. no need to store 4 bytes for hit count anymore, we can just compute color and store that!
	childrenThatExist uint8 // bitmask. 00 01 10 11. least significant bit is 00, most significant bit is 11.
}

type PackedSparseQuadtree []PackedSparseQuadtreeEntry

func createPackedTree(tree *SparseQuadtree) PackedSparseQuadtree {
	s := make(PackedSparseQuadtree, 0)
	var scalePow int64 = 1
	for i := 1; i < tree.levels; i++ {
		scalePow *= 4
	}
	pack(tree, 0, &s, scalePow)

	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 { // reverse a slice, pasted from stackoverflow lol
		s[i], s[j] = s[j], s[i]
	}

	return s
}

func pack(tree *SparseQuadtree, ptr NodePtr, output *PackedSparseQuadtree, scalePow int64) {
	startPos := len(*output)
	var bitMask uint8
	for i := 0; i < 4; i++ {
		if tree.nodes[ptr].Children[i] != -1 {
			pack(tree, tree.nodes[ptr].Children[i], output, scalePow>>2)
			bitMask |= 1 << i
		}
	}
	endPos := len(*output)
	*output = append(*output, PackedSparseQuadtreeEntry{
		cumulativeQuantityOfDescendents: endPos - startPos,
		grayscale: hitCntToHeat(tree.nodes[ptr].hits, scalePow),
		childrenThatExist: bitMask,
	})
}

type HybridNode struct {
	miss                bool
	inDense             bool
	index               int
	quadrant            int
	recursingIntoCorner bool
}

func getHit(ptr HybridNode, sparse SparseQuadtree, dense DenseQuadtree) Hits {
	if ptr.miss {
		panic("no")
	}
	if ptr.inDense {
		return dense.tree[ptr.index]
	} else {
		return sparse.nodes[ptr.index].hits
	}
}

func traverse(ptr HybridNode, dir int, sparse SparseQuadtree) HybridNode {
	if ptr.miss {
		return ptr
	}
	if ptr.inDense {
		ptr.index = ptr.index<<2 + 1 + dir
		return ptr
	}
	if ptr.quadrant == 0 && !ptr.recursingIntoCorner {
		ptr.quadrant = dir // the first traverse call establishes our overall quadrant
		ptr.recursingIntoCorner = true
	} else {
		if ptr.quadrant+dir != 3 {
			// went off course, therefore recursing into the dense tree is no longer on the table
			ptr.recursingIntoCorner = false
			ptr.quadrant = -1 // prevent reinitializing recursion status
		}
	}
	child := sparse.nodes[ptr.index].Children[dir]
	if child == -1 {
		// either miss, or transition to dense quad tree near spawn!
		if ptr.recursingIntoCorner {
			// transition to dense quad tree
			ptr.index = 1 + 3 - dir // add 1 to go from root to top level subnode (to select quadrant), and subtract from 3 to go into opposite quadrant because this is root
			ptr.inDense = true
			return ptr
		}
		ptr.miss = true
		return ptr
	}
	ptr.index = int(child)
	return ptr
}

type Hits uint32

type DenseQuadtree struct {
	tree   []Hits
	levels int
	width  int
}

func createDense(levels int) DenseQuadtree {
	if levels < 1 {
		panic("no")
	}
	sz := 1
	szAtLevel := 1
	width := 1
	for i := 1; i < levels; i++ {
		szAtLevel *= 4
		width *= 2
		sz += szAtLevel
	}
	return DenseQuadtree{levels: levels, tree: make([]Hits, sz), width: width}
}

func (tree *DenseQuadtree) walkAndIncrement(x int, y int, cnt int) {
	ind := 0
	tree.applyHits(ind, cnt)
	for i := tree.levels - 2; i >= 0; i-- {
		ind = ind<<2 + 1 + ((x >> i) & 1) + ((y>>i)&1)<<1
		tree.applyHits(ind, cnt)
	}
}

func (tree *DenseQuadtree) applyHits(ind int, cnt int) {
	orig := tree.tree[ind]
	sum := orig + Hits(cnt)
	if sum < orig {
		panic("overflow")
	}
	tree.tree[ind] = sum
}

func load(path string) []HitData {
	file, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	// some golang trickery to multithread the parsing of the csv lol
	// theres nothing actually fancy here, it's just reading the csv and returning []HitData
	chunks := make(chan io.Reader)
	hitsCh := make(chan []HitData)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chunk := range chunks {
				scanner := bufio.NewScanner(chunk)
				result := make([]HitData, 0)
				for scanner.Scan() {
					line := strings.Split(scanner.Text(), ",")
					if len(line) != 3 {
						panic("bad input")
					}
					x, err := strconv.Atoi(line[0])
					if err != nil {
						panic(err)
					}
					z, err := strconv.Atoi(line[1])
					if err != nil {
						panic(err)
					}
					cnt, err := strconv.Atoi(line[2])
					if err != nil {
						panic(err)
					}
					result = append(result, HitData{x, z, cnt})
				}
				if err := scanner.Err(); err != nil {
					panic(err)
				}
				hitsCh <- result
			}
		}()
	}
	go func() {
		wg.Wait()
		close(hitsCh)
	}()
	go loadFileIntoChunks(chunks, file)
	allHits := make([]HitData, 0, 200000000)
	for hits := range hitsCh {
		allHits = append(allHits, hits...)
	}
	return allHits
}

const BUF_SZ = 1000000

func loadFileIntoChunks(chunks chan io.Reader, file *os.File) {
	defer close(chunks)
	oneByteBuf := make([]byte, 1)
	for {
		buf := make([]byte, BUF_SZ, BUF_SZ+100)
		n, err := file.Read(buf)
		if err != nil && err != io.EOF {
			panic(err)
		}
		if n == 0 && err == io.EOF {
			return
		}
		buf = buf[:n]
		if n == BUF_SZ {
			for buf[len(buf)-1] != '\n' { // keep reading until newline
				_, err := file.Read(oneByteBuf)
				if err != nil {
					panic(err) // assume file ends in newline
				}
				buf = append(buf, oneByteBuf[0])
			}
		}
		if buf[len(buf)-1] != '\n' {
			panic("last byte of file wasnt newline")
		}
		chunks <- bytes.NewBuffer(buf)
	}
}
