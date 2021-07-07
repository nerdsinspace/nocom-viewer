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

	echo "github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	hits := load()
	log.Println("Loaded", len(hits), "hits")
	denseSpawn := createDense(15)
	width := denseSpawn.width
	log.Println("Tree width", width)
	offset := width / 2
	totalHitsInDense := 0
	for _, hit := range hits {
		x := hit.x + offset
		z := hit.z + offset
		if x >= 0 && x < width && z >= 0 && z < width {
			totalHitsInDense += hit.cnt
			denseSpawn.walkAndIncrement(x, z, hit.cnt)
		}
	}
	log.Println("Total hits in dense tree", totalHitsInDense)
	log.Println("Expect 65535 to the root:", denseSpawn.tree[0])

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
						data := render(path, denseSpawn, 9)
						return c.Blob(http.StatusOK, "image/png", data)
					}

					// /nocom/topdown/tl/3/2.png
					//return c.Redirect(http.StatusTemporaryRedirect, "https://cloud.daporkchop.net/minecraft/2b2t/map/2018/100k/topdown/tl"+p1)
				}
			}
			return next(c)
		}
	})

	e.Static("/", "static")
	e.Logger.Fatal(e.Start(":4679"))
}

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

var grayscale = make([]color.RGBA, 256)

func init() {
	for i := 0; i < 256; i++ {
		grayscale[i] = color.RGBA{uint8(i), uint8(i), uint8(i), 255}
	}
}

func hitCntToColor(cnt Hits, scalePow int) color.RGBA {
	if cnt == 0 {
		return grayscale[255]
	}
	val := math.Log(float64(cnt) / float64(scalePow))
	if val < 0 {
		val = 0
	}
	val *= 50
	val += 50
	if val > 255 {
		val = 255
	}
	return grayscale[255-int(val)]
}

func render(path []int, quadtree DenseQuadtree, levelSz int) []byte {
	// empty path = entire tree
	// levelSz = 1 means 1x1
	// levelSz = 2 means 2x2
	// levelSz = 3 means 4x4
	/*if len(path) + levelSz > quadtree.levels {
		panic("what")
	}*/
	tooBigBy := len(path) + levelSz - quadtree.levels
	log.Println("Raw tbb", tooBigBy)
	extraNodeLevelsPerPixel := 0
	if tooBigBy < 0 {
		extraNodeLevelsPerPixel = -tooBigBy
		tooBigBy = 0 // 1 node is SMALLER THAN one pixel
	} // if equal, one node EQUALS one pixel
	imgSz := 1
	for i := 1; i < levelSz; i++ {
		imgSz *= 2
	}
	scaleAmt := 1
	for i := 0; i < extraNodeLevelsPerPixel; i++ {
		scaleAmt *= 4
	}
	log.Println("Scale is", scaleAmt)
	locationToDraw := 0
	for _, item := range path {
		item -= 1                                                  // leaflet uses 1234 but we prefer 0123
		locationToDraw = indDownOne(locationToDraw, item, item>>1) // for this reason :)
	}
	ret := image.NewRGBA(image.Rectangle{image.Point{0, 0}, image.Point{imgSz, imgSz}})
	for y := 0; y < imgSz; y++ {
		for x := 0; x < imgSz; x++ {
			pos := locationToDraw
			for i := levelSz - 2; i >= tooBigBy; i-- {
				pos = indDownOne(pos, x>>i, y>>i)
			}
			ret.SetRGBA(x, y, hitCntToColor(quadtree.tree[pos], scaleAmt))
		}
	}
	var buf bytes.Buffer
	err := png.Encode(&buf, ret)
	if err != nil {
		panic(err)
	}
	return buf.Bytes()
}

type HitData struct {
	x   int
	z   int
	cnt int
}

type NodePtr int32
type QuadtreeNode struct {
	NN   NodePtr
	NP   NodePtr
	PN   NodePtr
	PP   NodePtr
	hits Hits
}

type Quadtree struct {
	root  NodePtr
	nodes []QuadtreeNode
}

/*type Hits uint16
const HITS_MAX int = 65535*/
type Hits uint32

const HITS_MAX int = 4294967295

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
	/*if x < 0 || y < 0 || y >= tree.width || x >= tree.width {
		panic("what")
	}*/
	ind := 0
	tree.applyHits(ind, cnt)
	//tree.applyAtLeastOne(ind)
	for i := tree.levels - 2; i >= 0; i-- {
		ind = indDownOne(ind, x>>i, y>>i)
		tree.applyHits(ind, cnt)
		//tree.applyAtLeastOne(ind)
	}
	//tree.applyHits(ind, cnt-1)
}
func (tree *DenseQuadtree) applyAtLeastOne(ind int) {
	if tree.tree[ind] == 0 {
		tree.tree[ind]++
	}
}

func (tree *DenseQuadtree) applyHits(ind int, cnt int) {
	var sum int
	sum += int(tree.tree[ind])
	sum += cnt
	if sum > HITS_MAX {
		sum = HITS_MAX
	}
	tree.tree[ind] = Hits(sum)
	/*if int(tree.tree[ind]) != sum {
		panic("what")
	}*/
}

func indUpOne(ind int) int {
	return (ind - 1) >> 2
}

func indDownOne(ind int, x int, y int) int {
	return ind<<2 + 1 + (x & 1) + (y&1)<<1
}

func load() []HitData {
	file, err := os.Open("/Users/leijurv/Downloads/heatmap_full.csv")
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

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
					/*if len(result)%100000 == 0 {
						log.Println(len(result))
					}*/
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
	/*go func(){
		defer close(chunks)
		buf, err := ioutil.ReadAll(file)
		if err != nil {
			panic(err)
		}
		chunks<-bytes.NewBuffer(buf)
	}()*/
	allHits := make([]HitData, 0, 200000000)
	for hits := range hitsCh {
		allHits = append(allHits, hits...)
		//log.Println("uwu",len(allHits))
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
		if n == 0 {
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
