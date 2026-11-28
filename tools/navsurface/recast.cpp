// Build with the official Recast/Detour v1.6.0 libraries (commit b4554541).
// Input and output are local asset data. The live client never invokes this tool.
#include <Recast.h>
#include <DetourNavMeshBuilder.h>
#include <DetourAlloc.h>
#include <cmath>
#include <cstdint>
#include <cstdio>
#include <memory>
#include <stdexcept>
#include <vector>

#ifndef DT_POLYREF64
#error "Build with DT_POLYREF64, matching the client's existing Detour reader."
#endif

template<class T> void read(T* values, std::size_t count) {
    if (std::fread(values, sizeof(T), count, stdin) != count)
        throw std::runtime_error("incomplete surface geometry input");
}

int main() {
    try {
        std::uint32_t header[3];
        read(header, 3);
        if (header[0] != 0x31535741 || header[1] < 3 || header[1] > 1000000 ||
            header[2] < 1 || header[2] > 1000000)
            throw std::runtime_error("invalid surface geometry header");
        float input[6]; // body radius/height/climb, slope, cell width/height
        read(input, 6);
        for (float number : input)
            if (!std::isfinite(number) || number <= 0)
                throw std::runtime_error("invalid navigation body settings");
        std::vector<float> vertices(header[1] * 3);
        std::vector<int> triangles(header[2] * 3);
        read(vertices.data(), vertices.size());
        read(triangles.data(), triangles.size());
        if (std::getchar() != EOF)
            throw std::runtime_error("trailing geometry input");
        for (float coordinate : vertices)
            if (!std::isfinite(coordinate))
                throw std::runtime_error("nonfinite surface vertex");
        for (int index : triangles)
            if (index < 0 || std::uint32_t(index) >= header[1])
                throw std::runtime_error("invalid surface triangle");

        rcContext context;
        rcConfig config{};
        config.cs = input[4];
        config.ch = input[5];
        config.walkableSlopeAngle = input[3];
        config.walkableHeight = int(std::ceil(input[1] / config.ch));
        config.walkableClimb = int(std::floor(input[2] / config.ch));
        config.walkableRadius = int(std::ceil(input[0] / config.cs));
        config.maxEdgeLen = int(12 / config.cs);
        config.maxSimplificationError = 1.3f;
        config.minRegionArea = 0;
        config.mergeRegionArea = 20 * 20;
        config.maxVertsPerPoly = 6;
        config.detailSampleDist = config.cs * 6;
        config.detailSampleMaxError = config.ch;
        rcCalcBounds(vertices.data(), int(header[1]), config.bmin, config.bmax);
        // Include headroom above the highest deck and exact collision below it.
        config.bmin[1] -= config.ch;
        config.bmax[1] += input[1] + config.ch;
        rcCalcGridSize(config.bmin, config.bmax, config.cs, &config.width, &config.height);
        if (config.width <= 0 || config.height <= 0 ||
            std::uint64_t(config.width) * config.height > 16000000)
            throw std::runtime_error("moving surface raster exceeds its model envelope");

        std::unique_ptr<rcHeightfield, decltype(&rcFreeHeightField)> heightfield(rcAllocHeightfield(), rcFreeHeightField);
        std::unique_ptr<rcCompactHeightfield, decltype(&rcFreeCompactHeightfield)> compact(rcAllocCompactHeightfield(), rcFreeCompactHeightfield);
        std::unique_ptr<rcContourSet, decltype(&rcFreeContourSet)> contours(rcAllocContourSet(), rcFreeContourSet);
        std::unique_ptr<rcPolyMesh, decltype(&rcFreePolyMesh)> mesh(rcAllocPolyMesh(), rcFreePolyMesh);
        std::unique_ptr<rcPolyMeshDetail, decltype(&rcFreePolyMeshDetail)> detail(rcAllocPolyMeshDetail(), rcFreePolyMeshDetail);
        if (!heightfield || !compact || !contours || !mesh || !detail)
            throw std::runtime_error("cannot allocate Recast working data");
        if (!rcCreateHeightfield(&context, *heightfield, config.width, config.height,
                                config.bmin, config.bmax, config.cs, config.ch))
            throw std::runtime_error("cannot create surface heightfield");
        std::vector<unsigned char> areas(header[2], RC_NULL_AREA);
        rcMarkWalkableTriangles(&context, config.walkableSlopeAngle, vertices.data(), int(header[1]),
                               triangles.data(), int(header[2]), areas.data());
        if (!rcRasterizeTriangles(&context, vertices.data(), int(header[1]), triangles.data(), areas.data(),
                                  int(header[2]), *heightfield, config.walkableClimb))
            throw std::runtime_error("cannot rasterize moving surface");
        rcFilterLowHangingWalkableObstacles(&context, config.walkableClimb, *heightfield);
        rcFilterLedgeSpans(&context, config.walkableHeight, config.walkableClimb, *heightfield);
        rcFilterWalkableLowHeightSpans(&context, config.walkableHeight, *heightfield);
        if (!rcBuildCompactHeightfield(&context, config.walkableHeight, config.walkableClimb, *heightfield, *compact) ||
            !rcErodeWalkableArea(&context, config.walkableRadius, *compact) ||
            !rcBuildDistanceField(&context, *compact) ||
            !rcBuildRegions(&context, *compact, 0, config.minRegionArea, config.mergeRegionArea) ||
            !rcBuildContours(&context, *compact, config.maxSimplificationError, config.maxEdgeLen, *contours) ||
            !rcBuildPolyMesh(&context, *contours, config.maxVertsPerPoly, *mesh) ||
            !rcBuildPolyMeshDetail(&context, *mesh, *compact, config.detailSampleDist, config.detailSampleMaxError, *detail))
            throw std::runtime_error("cannot build the surface polygon mesh");
        if (mesh->npolys == 0)
            throw std::runtime_error("model has no walkable surface for this body");
        for (int i = 0; i < mesh->npolys; ++i) {
            mesh->flags[i] = 1;
            mesh->areas[i] = 1;
        }
        dtNavMeshCreateParams params{};
        params.verts = mesh->verts;
        params.vertCount = mesh->nverts;
        params.polys = mesh->polys;
        params.polyAreas = mesh->areas;
        params.polyFlags = mesh->flags;
        params.polyCount = mesh->npolys;
        params.nvp = mesh->nvp;
        params.detailMeshes = detail->meshes;
        params.detailVerts = detail->verts;
        params.detailVertsCount = detail->nverts;
        params.detailTris = detail->tris;
        params.detailTriCount = detail->ntris;
        params.walkableRadius = input[0];
        params.walkableHeight = input[1];
        params.walkableClimb = input[2];
        params.cs = config.cs;
        params.ch = config.ch;
        for (int axis = 0; axis < 3; ++axis) {
            params.bmin[axis] = mesh->bmin[axis];
            params.bmax[axis] = mesh->bmax[axis];
        }
        params.buildBvTree = true;
        unsigned char* data = nullptr;
        int bytes = 0;
        if (!dtCreateNavMeshData(&params, &data, &bytes))
            throw std::runtime_error("cannot serialize Detour surface tile");
        std::unique_ptr<unsigned char, decltype(&dtFree)> owned(data, dtFree);
        if (std::fwrite(data, 1, bytes, stdout) != std::size_t(bytes) || std::fflush(stdout))
            throw std::runtime_error("cannot write Detour surface tile");
        std::fprintf(stderr, "%d polygons, %d vertices, %d bytes\n", mesh->npolys, mesh->nverts, bytes);
        return 0;
    } catch (const std::exception& error) {
        std::fprintf(stderr, "%s\n", error.what());
        return 1;
    }
}
