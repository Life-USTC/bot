//! Sandboxed typst `World` implementation.
//!
//! The world serves exactly one in-memory source file and a lazily-loaded,
//! process-global font set. It never touches the filesystem or the network
//! at compile time, keeping the sandbox semantics: requests cannot read
//! arbitrary files or fetch packages.

use std::path::PathBuf;
use std::sync::OnceLock;
use std::time::{SystemTime, UNIX_EPOCH};

use typst::diag::{FileError, FileResult};
use typst::foundations::{Bytes, Datetime};
use typst::syntax::{FileId, Source, VirtualPath};
use typst::text::{Font, FontBook};
use typst::utils::LazyHash;
use typst::{Library, World};

/// Process-global font set, loaded once from disk and shared (read-only)
/// across all requests. `Font` is internally reference counted, so cloning
/// a handle per request is cheap.
pub struct FontSet {
    book: LazyHash<FontBook>,
    fonts: Vec<Font>,
}

static FONTS: OnceLock<FontSet> = OnceLock::new();

/// Directories scanned for fonts. `RENDERD_FONT_DIR` (if set) is scanned
/// first; fonts from all existing directories are merged.
const FONT_DIR_CANDIDATES: &[&str] = &[
    "/usr/share/fonts/adobe-source-han-sans-cn-fonts",
    "/usr/share/fonts/google-noto-sans-cjk-fonts",
    "/usr/share/fonts/opentype/noto",
    "/usr/share/fonts/fira-code",
    "/usr/share/fonts/truetype/firacode",
];

/// Load (once) the global font set. Returns an error message if no usable
/// font was found.
pub fn fonts() -> Result<&'static FontSet, String> {
    // OnceLock::get_or_try_init is unstable; emulate it by caching a possible
    // error string alongside.
    static FONT_ERROR: OnceLock<Option<String>> = OnceLock::new();

    if let Some(set) = FONTS.get() {
        return Ok(set);
    }
    if let Some(err) = FONT_ERROR.get() {
        if let Some(err) = err {
            return Err(err.clone());
        }
    }

    match load_fonts() {
        Ok(set) => {
            let _ = FONTS.set(set);
            Ok(FONTS.get().expect("font set just initialized"))
        }
        Err(err) => {
            let _ = FONT_ERROR.set(Some(err.clone()));
            Err(err)
        }
    }
}

fn load_fonts() -> Result<FontSet, String> {
    let mut dirs: Vec<PathBuf> = Vec::new();
    if let Ok(dir) = std::env::var("RENDERD_FONT_DIR") {
        if !dir.trim().is_empty() {
            dirs.push(PathBuf::from(dir));
        }
    }
    dirs.extend(FONT_DIR_CANDIDATES.iter().map(PathBuf::from));

    let mut fonts: Vec<Font> = Vec::new();
    let mut scanned = Vec::new();
    for dir in &dirs {
        scanned.push(dir.display().to_string());
        let entries = match std::fs::read_dir(dir) {
            Ok(entries) => entries,
            Err(_) => continue,
        };
        let mut count = 0usize;
        for entry in entries.flatten() {
            let path = entry.path();
            let is_font = path
                .extension()
                .and_then(|e| e.to_str())
                .map(|e| matches!(e.to_ascii_lowercase().as_str(), "otf" | "ttf" | "ttc" | "otc"))
                .unwrap_or(false);
            if !is_font {
                continue;
            }
            let data = match std::fs::read(&path) {
                Ok(data) => data,
                Err(_) => continue,
            };
            let before = fonts.len();
            fonts.extend(Font::iter(Bytes::new(data)));
            count += fonts.len() - before;
        }
        if count > 0 {
            tracing::info!(dir = %dir.display(), count, "loaded fonts");
        }
    }
    if fonts.is_empty() {
        return Err(format!("no fonts found in {}", scanned.join(", ")));
    }

    let book = FontBook::from_fonts(fonts.iter());
    Ok(FontSet { book: LazyHash::new(book), fonts })
}

/// One-off world for a single compile. Cheap to construct: the library is
/// static-ish and fonts are shared. All per-request state lives here, so
/// concurrent compiles never share mutable state.
pub struct SandboxWorld {
    library: LazyHash<Library>,
    main_id: FileId,
    main: Source,
}

impl SandboxWorld {
    pub fn new(source_text: String) -> Self {
        let main_id = FileId::new(None, VirtualPath::new("main.typ"));
        let main = Source::new(main_id, source_text);
        Self { library: LazyHash::new(Library::default()), main_id, main }
    }
}

impl World for SandboxWorld {
    fn library(&self) -> &LazyHash<Library> {
        &self.library
    }

    fn book(&self) -> &LazyHash<FontBook> {
        // Failure here was already surfaced during startup warm-up; if a
        // request somehow races it, fall back to an empty book (compile will
        // report missing fonts rather than panic).
        match fonts() {
            Ok(set) => &set.book,
            Err(_) => {
                static EMPTY: OnceLock<LazyHash<FontBook>> = OnceLock::new();
                EMPTY.get_or_init(|| LazyHash::new(FontBook::new()))
            }
        }
    }

    fn main(&self) -> FileId {
        self.main_id
    }

    fn source(&self, id: FileId) -> FileResult<Source> {
        if id == self.main_id {
            Ok(self.main.clone())
        } else if id.vpath().as_rootless_path().to_str() == Some("common.typ") {
            // Shared template fragment (palette, metrics, watermark, footer)
            // imported by every card template; embedded like the logo assets.
            Ok(Source::new(id, COMMON_TYP.to_string()))
        } else {
            Err(FileError::NotFound(id.vpath().as_rootless_path().into()))
        }
    }

    fn file(&self, id: FileId) -> FileResult<Bytes> {
        match id.vpath().as_rootless_path().to_str() {
            Some("assets/logo.png") => Ok(raw_logo().clone()),
            Some("assets/logo-15.png") => faded_logo(0.15)
                .cloned()
                .ok_or_else(|| FileError::NotFound(id.vpath().as_rootless_path().into())),
            Some("assets/logo-10.png") => faded_logo(0.10)
                .cloned()
                .ok_or_else(|| FileError::NotFound(id.vpath().as_rootless_path().into())),
            _ => Err(FileError::NotFound(id.vpath().as_rootless_path().into())),
        }
    }

    fn font(&self, index: usize) -> Option<Font> {
        fonts().ok()?.fonts.get(index).cloned()
    }

    fn today(&self, offset: Option<i64>) -> Option<Datetime> {
        let secs = SystemTime::now().duration_since(UNIX_EPOCH).ok()?.as_secs() as i64;
        let shifted = secs + offset.unwrap_or(0) * 3600;
        let days = shifted.div_euclid(86_400);
        let (year, month, day) = civil_from_days(days);
        Datetime::from_ymd(year, month, day)
    }
}

/// The embedded Life@USTC logo, served to templates as `assets/logo.png`.
fn raw_logo() -> &'static Bytes {
    static LOGO: OnceLock<Bytes> = OnceLock::new();
    LOGO.get_or_init(|| Bytes::new(include_bytes!("../assets/life_ustc_logo_raw.png").to_vec()))
}

/// Shared template fragment, served to card templates as `common.typ`.
const COMMON_TYP: &str = include_str!("templates/common.typ");

/// The logo with its alpha pre-scaled, served as `assets/logo-15.png` etc.
/// Typst cannot draw images at reduced opacity, so the fade is baked into the
/// PNG once and cached. Returns None if the logo cannot be decoded; the
/// template then fails to compile, which is surfaced as a render error.
fn faded_logo(opacity: f32) -> Option<&'static Bytes> {
    fn build(opacity: f32) -> Option<Bytes> {
        let mut pixmap = tiny_skia::Pixmap::decode_png(raw_logo()).ok()?;
        for pixel in pixmap.pixels_mut() {
            let c = pixel.demultiply();
            let alpha = (c.alpha() as f32 * opacity).round() as u8;
            *pixel = tiny_skia::ColorU8::from_rgba(c.red(), c.green(), c.blue(), alpha)
                .premultiply();
        }
        pixmap.encode_png().ok().map(Bytes::new)
    }

    static LOGO15: OnceLock<Option<Bytes>> = OnceLock::new();
    static LOGO10: OnceLock<Option<Bytes>> = OnceLock::new();
    if (opacity - 0.15).abs() < f32::EPSILON {
        return LOGO15.get_or_init(|| build(0.15)).as_ref();
    }
    if (opacity - 0.10).abs() < f32::EPSILON {
        return LOGO10.get_or_init(|| build(0.10)).as_ref();
    }
    None
}

/// Pre-generate derived assets (faded logo variants) so the first request
/// does not pay for PNG decode/encode, and fail fast if the logo is broken.
pub fn warm_assets() -> Result<(), String> {
    faded_logo(0.15)
        .and_then(|_| faded_logo(0.10))
        .map(|_| ())
        .ok_or_else(|| "failed to pre-render faded logo variants".to_string())
}

/// Howard Hinnant's civil-from-days algorithm.
fn civil_from_days(z: i64) -> (i32, u8, u8) {    let z = z + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    (y as i32, m as u8, d as u8)
}

#[cfg(test)]
mod debug_compile_tests {
    /// Compile the typst file named by DEBUG_TYP_FILE and print diagnostics
    /// with line numbers. Used to debug generated template syntax quickly:
    /// `DEBUG_TYP_FILE=/tmp/gen.typ cargo test debug_compile -- --nocapture`.
    #[test]
    fn debug_compile() {
        let Ok(path) = std::env::var("DEBUG_TYP_FILE") else {
            return;
        };
        let source = std::fs::read_to_string(path).expect("read debug file");
        let world = super::SandboxWorld::new(source);
        let warned = typst::compile::<typst::layout::PagedDocument>(&world);
        use typst::World as _;
        let main = world.source(world.main()).unwrap();
        match warned.output {
            Ok(_) => println!("COMPILE OK"),
            Err(errors) => {
                for e in &errors {
                    let line = main
                        .range(e.span)
                        .and_then(|r| main.byte_to_line(r.start))
                        .map(|l| l + 1);
                    eprintln!("ERR line={:?}: {}", line, e.message);
                    for hint in &e.hints {
                        eprintln!("  hint: {hint}");
                    }
                }
                panic!("compile failed");
            }
        }
    }
}
