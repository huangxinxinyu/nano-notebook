import { useState } from "react";

export type SourceImageRegion = {
  x?: number;
  y?: number;
  width?: number;
  height?: number;
};

export function SourceImageViewer({ sourceID, title, regions }: { sourceID: string; title: string; regions: SourceImageRegion[] }) {
  const [dimensions, setDimensions] = useState<{ width: number; height: number } | null>(null);
  return (
    <div className="source-image-viewer">
      <img
        alt={title}
        draggable={false}
        src={`/api/v1/sources/${sourceID}/viewer-asset`}
        onLoad={(event) => {
          const image = event.currentTarget;
          if (image.naturalWidth > 0 && image.naturalHeight > 0) setDimensions({ width: image.naturalWidth, height: image.naturalHeight });
        }}
      />
      {dimensions ? regions.map((region, index) => validRegion(region, dimensions) ? (
        <span
          aria-hidden="true"
          className="source-image-highlight"
          key={index}
          style={{
            left: `${region.x! / dimensions.width * 100}%`,
            top: `${region.y! / dimensions.height * 100}%`,
            width: `${region.width! / dimensions.width * 100}%`,
            height: `${region.height! / dimensions.height * 100}%`
          }}
        />
      ) : null) : null}
    </div>
  );
}

function validRegion(region: SourceImageRegion, dimensions: { width: number; height: number }) {
  return typeof region.x === "number" && typeof region.y === "number" && typeof region.width === "number" && typeof region.height === "number" &&
    region.x >= 0 && region.y >= 0 && region.width > 0 && region.height > 0 &&
    region.x + region.width <= dimensions.width && region.y + region.height <= dimensions.height;
}
