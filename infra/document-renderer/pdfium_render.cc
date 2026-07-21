#include <zlib.h>

#include <cmath>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <iostream>
#include <limits>
#include <string>
#include <vector>

#include "fpdfview.h"

namespace {

class PdfiumLibrary {
 public:
  PdfiumLibrary() { FPDF_InitLibrary(); }
  ~PdfiumLibrary() { FPDF_DestroyLibrary(); }
};

class Document {
 public:
  explicit Document(const char* path) : value_(FPDF_LoadDocument(path, nullptr)) {}
  ~Document() {
    if (value_ != nullptr) FPDF_CloseDocument(value_);
  }
  FPDF_DOCUMENT get() const { return value_; }

 private:
  FPDF_DOCUMENT value_;
};

class Page {
 public:
  Page(FPDF_DOCUMENT document, int index) : value_(FPDF_LoadPage(document, index)) {}
  ~Page() {
    if (value_ != nullptr) FPDF_ClosePage(value_);
  }
  FPDF_PAGE get() const { return value_; }

 private:
  FPDF_PAGE value_;
};

class Bitmap {
 public:
  Bitmap(int width, int height) : value_(FPDFBitmap_Create(width, height, 1)) {}
  ~Bitmap() {
    if (value_ != nullptr) FPDFBitmap_Destroy(value_);
  }
  FPDF_BITMAP get() const { return value_; }

 private:
  FPDF_BITMAP value_;
};

bool ParsePositiveDouble(const std::string& value, double* output) {
  try {
    size_t parsed = 0;
    *output = std::stod(value, &parsed);
    return parsed == value.size() && std::isfinite(*output) && *output > 0 && *output <= 10;
  } catch (...) {
    return false;
  }
}

bool ParsePositiveInt64(const std::string& value, int64_t* output) {
  try {
    size_t parsed = 0;
    *output = std::stoll(value, &parsed);
    return parsed == value.size() && *output > 0;
  } catch (...) {
    return false;
  }
}

void AppendBigEndian(std::vector<uint8_t>* output, uint32_t value) {
  output->push_back(static_cast<uint8_t>(value >> 24));
  output->push_back(static_cast<uint8_t>(value >> 16));
  output->push_back(static_cast<uint8_t>(value >> 8));
  output->push_back(static_cast<uint8_t>(value));
}

void AppendChunk(std::vector<uint8_t>* output, const char type[4], const std::vector<uint8_t>& data) {
  AppendBigEndian(output, static_cast<uint32_t>(data.size()));
  output->insert(output->end(), type, type + 4);
  output->insert(output->end(), data.begin(), data.end());
  uLong checksum = crc32(0L, Z_NULL, 0);
  checksum = crc32(checksum, reinterpret_cast<const Bytef*>(type), 4);
  if (!data.empty()) checksum = crc32(checksum, data.data(), data.size());
  AppendBigEndian(output, static_cast<uint32_t>(checksum));
}

bool WritePNG(const std::string& path, FPDF_BITMAP bitmap, int width, int height) {
  const auto* buffer = static_cast<const uint8_t*>(FPDFBitmap_GetBuffer(bitmap));
  const int stride = FPDFBitmap_GetStride(bitmap);
  if (buffer == nullptr || stride < width * 4) return false;
  const size_t row_bytes = static_cast<size_t>(width) * 4;
  if (static_cast<size_t>(height) > std::numeric_limits<size_t>::max() / (row_bytes + 1)) return false;
  std::vector<uint8_t> raw(static_cast<size_t>(height) * (row_bytes + 1));
  for (int y = 0; y < height; ++y) {
    uint8_t* destination = raw.data() + static_cast<size_t>(y) * (row_bytes + 1);
    destination[0] = 0;
    const uint8_t* source = buffer + static_cast<size_t>(y) * stride;
    for (int x = 0; x < width; ++x) {
      destination[1 + x * 4] = source[x * 4 + 2];
      destination[1 + x * 4 + 1] = source[x * 4 + 1];
      destination[1 + x * 4 + 2] = source[x * 4];
      destination[1 + x * 4 + 3] = source[x * 4 + 3];
    }
  }
  uLongf compressed_size = compressBound(raw.size());
  std::vector<uint8_t> compressed(compressed_size);
  if (compress2(compressed.data(), &compressed_size, raw.data(), raw.size(), Z_BEST_SPEED) != Z_OK) return false;
  compressed.resize(compressed_size);

  std::vector<uint8_t> png = {137, 80, 78, 71, 13, 10, 26, 10};
  std::vector<uint8_t> header;
  AppendBigEndian(&header, static_cast<uint32_t>(width));
  AppendBigEndian(&header, static_cast<uint32_t>(height));
  header.insert(header.end(), {8, 6, 0, 0, 0});
  AppendChunk(&png, "IHDR", header);
  AppendChunk(&png, "IDAT", compressed);
  AppendChunk(&png, "IEND", {});
  std::ofstream file(path, std::ios::binary | std::ios::trunc);
  file.write(reinterpret_cast<const char*>(png.data()), static_cast<std::streamsize>(png.size()));
  return file.good();
}

}  // namespace

int main(int argc, char** argv) {
  if (argc != 5 || std::string(argv[1]) != "--png") {
    std::cerr << "usage: pdfium_render --png --scale=N --max-pixels=N input.pdf\n";
    return 2;
  }
  const std::string scale_arg = argv[2];
  const std::string pixels_arg = argv[3];
  if (scale_arg.rfind("--scale=", 0) != 0 || pixels_arg.rfind("--max-pixels=", 0) != 0) return 2;
  double scale = 0;
  int64_t max_pixels = 0;
  if (!ParsePositiveDouble(scale_arg.substr(8), &scale) || !ParsePositiveInt64(pixels_arg.substr(13), &max_pixels)) return 2;

  PdfiumLibrary library;
  Document document(argv[4]);
  if (document.get() == nullptr) {
    std::cerr << "PDFium rejected the document, error=" << FPDF_GetLastError() << "\n";
    return 3;
  }
  const int pages = FPDF_GetPageCount(document.get());
  if (pages < 1) return 3;
  for (int index = 0; index < pages; ++index) {
    Page page(document.get(), index);
    if (page.get() == nullptr) return 3;
    const double scaled_width = std::ceil(static_cast<double>(FPDF_GetPageWidthF(page.get())) * scale);
    const double scaled_height = std::ceil(static_cast<double>(FPDF_GetPageHeightF(page.get())) * scale);
    if (!std::isfinite(scaled_width) || !std::isfinite(scaled_height) || scaled_width < 1 || scaled_height < 1 ||
        scaled_width > std::numeric_limits<int>::max() || scaled_height > std::numeric_limits<int>::max()) return 4;
    const int width = static_cast<int>(scaled_width);
    const int height = static_cast<int>(scaled_height);
    if (static_cast<int64_t>(width) > max_pixels / static_cast<int64_t>(height)) return 4;
    Bitmap bitmap(width, height);
    if (bitmap.get() == nullptr) return 4;
    FPDFBitmap_FillRect(bitmap.get(), 0, 0, width, height, 0xFFFFFFFF);
    FPDF_RenderPageBitmap(bitmap.get(), page.get(), 0, 0, width, height, 0, FPDF_ANNOT);
    const std::string output = std::string(argv[4]) + "." + std::to_string(index) + ".png";
    if (!WritePNG(output, bitmap.get(), width, height)) return 5;
  }
  return 0;
}
