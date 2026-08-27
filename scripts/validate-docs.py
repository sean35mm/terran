#!/usr/bin/env python3
"""Validate the dependency-free static documentation site."""

from __future__ import annotations

import ipaddress
import os
import re
import stat
import sys
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit
from xml.etree import ElementTree


ROOT = Path(__file__).resolve().parents[1]
DOCS = ROOT / "docs"
ORIGIN = "https://sean35mm.github.io/terran/"
ORIGIN_PARTS = urlsplit(ORIGIN)
SITEMAP_URL = f"{ORIGIN}sitemap.xml"
ROBOTS_CONTENT = f"User-agent: *\nAllow: /\nSitemap: {SITEMAP_URL}\n"
EXPECTED_FILES = {
    Path(".nojekyll"),
    Path("index.html"),
    Path("404.html"),
    Path("robots.txt"),
    Path("sitemap.xml"),
    Path("assets/site.css"),
    Path("assets/favicon.svg"),
}
EXPECTED_DIRECTORIES = {Path("assets")}
REMOTE_IMAGE_URL = "https://static.wikia.nocookie.net/starcraft/images/d/dc/CommandCenter_SCR_Game1.png/revision/latest?cb=20220108145341"
REMOTE_IMAGE_HOST = "https://static.wikia.nocookie.net"
REMOTE_IMAGE_ALT = "StarCraft: Remastered Terran Command Center"
REMOTE_IMAGE_WIDTH = "511"
REMOTE_IMAGE_HEIGHT = "402"
REMOTE_IMAGE_CAPTION = (
    "StarCraft: Remastered Command Center image. Image and StarCraft are © Blizzard Entertainment. "
    "Terran is unaffiliated."
)
RESOURCE_ATTRS = {
    "audio": "src",
    "embed": "src",
    "iframe": "src",
    "img": "src",
    "input": "src",
    "link": "href",
    "object": "data",
    "script": "src",
    "source": "src",
    "track": "src",
    "video": "src",
}
FORBIDDEN_TAGS = {"script", "style", "form", "iframe", "object", "embed", "base", "audio", "video"}
CSP_BASE = {
    "default-src": ["'none'"],
    "style-src": ["'self'"],
    "img-src": ["'self'"],
    "script-src": ["'none'"],
    "font-src": ["'none'"],
    "connect-src": ["'none'"],
    "media-src": ["'none'"],
    "object-src": ["'none'"],
    "base-uri": ["'none'"],
    "form-action": ["'none'"],
}
SVG_NAMESPACE = "http://www.w3.org/2000/svg"
SVG_ALLOWED_TAGS = {
    "svg", "g", "path", "rect", "circle", "ellipse", "line", "polyline", "polygon", "title", "desc"
}
SVG_FORBIDDEN_TAGS = {
    "script", "foreignobject", "style", "image", "use", "a", "animate", "animatemotion", "animatetransform", "set", "discard"
}


class DocumentParser(HTMLParser):
    def __init__(self, path: Path, expected_canonical: str) -> None:
        super().__init__(convert_charrefs=True)
        self.path = path
        self.errors: list[str] = []
        self.ids: set[str] = set()
        self.fragments: list[str] = []
        self.local_urls: list[tuple[str, str, bool, bool]] = []
        self.main_count = 0
        self.h1_count = 0
        self.headings: list[int] = []
        self.lang = ""
        self.title_depth = 0
        self.title_text = ""
        self.description = False
        self.viewport = False
        self.canonical = ""
        self.canonical_count = 0
        self.csp = ""
        self.csp_count = 0
        self.referrer = ""
        self.referrer_count = 0
        self.robots = ""
        self.robots_count = 0
        self.expected_canonical = expected_canonical
        self.figure_depth = 0
        self.figcaption_depth = 0
        self.figcaption_text: list[str] = []
        self.image_count = 0
        self.approved_image_count = 0
        self.source_anchor_count = 0

    def error(self, message: str) -> None:
        self.errors.append(f"{self.path.relative_to(ROOT)}: {message}")

    def handle_starttag(self, tag: str, attrs_list: list[tuple[str, str | None]]) -> None:
        attrs = {key.lower(): (value or "") for key, value in attrs_list}
        tag = tag.lower()
        if tag == "html":
            self.lang = attrs.get("lang", "").strip()
        elif tag == "title":
            self.title_depth += 1
        elif tag == "main":
            self.main_count += 1
        elif tag == "h1":
            self.h1_count += 1
        elif tag == "figure":
            self.figure_depth += 1
        elif tag == "figcaption":
            self.figcaption_depth += 1
        if re.fullmatch(r"h[1-6]", tag):
            self.headings.append(int(tag[1]))
        if tag in FORBIDDEN_TAGS:
            self.error(f"forbidden <{tag}> element")
        for attribute in attrs:
            if attribute.startswith("on"):
                self.error(f"event-handler attribute is forbidden: {attribute}")
            if attribute == "style":
                self.error("inline style attributes are forbidden")
            if attribute in {"imagesrcset", "srcset"}:
                self.error(f"responsive remote resource attribute is forbidden: {attribute}")
        if tag == "img":
            self.image_count += 1
            if "alt" not in attrs:
                self.error("image is missing alt text")
            if attrs.get("src") == REMOTE_IMAGE_URL:
                self.approved_image_count += 1
                expected_attrs = {
                    "alt": REMOTE_IMAGE_ALT,
                    "width": REMOTE_IMAGE_WIDTH,
                    "height": REMOTE_IMAGE_HEIGHT,
                    "loading": "eager",
                    "decoding": "async",
                    "referrerpolicy": "no-referrer",
                }
                for attribute, expected in expected_attrs.items():
                    if attrs.get(attribute) != expected:
                        self.error(f"approved image requires {attribute}=\"{expected}\"")
                if self.path.name != "index.html" or not self.figure_depth:
                    self.error("approved image is allowed only in the index figure")
        if tag == "meta":
            name = attrs.get("name", "").lower()
            http_equiv = attrs.get("http-equiv", "").lower()
            if http_equiv == "refresh":
                self.error("meta refresh is forbidden")
            if name == "description" and attrs.get("content", "").strip():
                self.description = True
            if name == "viewport" and attrs.get("content", "").strip():
                self.viewport = True
            if name == "referrer":
                self.referrer_count += 1
                self.referrer = attrs.get("content", "").strip().lower()
            if name == "robots":
                self.robots_count += 1
                self.robots = attrs.get("content", "").replace(" ", "").lower()
            if http_equiv == "content-security-policy":
                self.csp_count += 1
                self.csp = attrs.get("content", "").strip()
        if tag == "link":
            rel = {part.lower() for part in attrs.get("rel", "").split()}
            if "canonical" in rel:
                if rel != {"canonical"}:
                    self.error("canonical link must not declare other relationships")
                self.canonical_count += 1
                self.canonical = attrs.get("href", "").strip()
        if tag == "a" and attrs.get("href") == REMOTE_IMAGE_URL:
            if self.path.name == "index.html" and self.figcaption_depth:
                self.source_anchor_count += 1
                if "noreferrer" not in {part.lower() for part in attrs.get("rel", "").split()}:
                    self.error("image source anchor requires rel=noreferrer")
            else:
                self.error("approved image URL is allowed as a link only in its figcaption")
        element_id = attrs.get("id")
        if element_id:
            if element_id in self.ids:
                self.error(f"duplicate id #{element_id}")
            self.ids.add(element_id)
        for attribute in ("data", "href", "poster", "src"):
            value = attrs.get(attribute)
            if not value:
                continue
            parsed = urlsplit(value)
            if value.startswith("//"):
                self.error(f"protocol-relative URL: {value}")
                continue
            if parsed.scheme == "http":
                self.error(f"insecure URL in {attribute}: {value}")
            if parsed.scheme.lower() in {"javascript", "data", "file"}:
                self.error(f"forbidden URL scheme in {attribute}: {value}")
            if parsed.hostname and is_local_host(parsed.hostname):
                self.error(f"local-only URL in {attribute}: {value}")
            link_rels = {part.lower() for part in attrs.get("rel", "").split()}
            resource_attribute = RESOURCE_ATTRS.get(tag)
            is_remote_resource = attribute == resource_attribute and not (tag == "link" and link_rels == {"canonical"})
            if is_remote_resource and parsed.scheme in {"http", "https", "//"}:
                approved_image = tag == "img" and attribute == "src" and value == REMOTE_IMAGE_URL and self.path.name == "index.html"
                if not approved_image:
                    self.error(f"remote resource in <{tag}>: {value}")
            if tag == "a" and parsed.scheme in {"http", "https"} and looks_like_image_url(value):
                approved_source = value == REMOTE_IMAGE_URL and self.path.name == "index.html" and self.figcaption_depth
                if not approved_source:
                    self.error(f"remote image link is forbidden: {value}")
            if parsed.scheme or parsed.netloc:
                continue
            if parsed.path:
                if parsed.path.startswith("/"):
                    if self.path.name != "404.html" or not parsed.path.startswith(ORIGIN_PARTS.path):
                        self.error(f"root-relative URL must stay under {ORIGIN_PARTS.path}: {value}")
                    else:
                        relative = unquote(parsed.path[len(ORIGIN_PARTS.path):])
                        self.local_urls.append((attribute, relative, parsed.path.endswith("/"), True))
                else:
                    self.local_urls.append((attribute, unquote(parsed.path), parsed.path.endswith("/"), False))
            if attribute == "href" and parsed.fragment and not parsed.path:
                self.fragments.append(unquote(parsed.fragment))

    def handle_endtag(self, tag: str) -> None:
        tag = tag.lower()
        if tag == "title" and self.title_depth:
            self.title_depth -= 1
        elif tag == "figcaption" and self.figcaption_depth:
            self.figcaption_depth -= 1
        elif tag == "figure" and self.figure_depth:
            self.figure_depth -= 1

    def handle_data(self, data: str) -> None:
        if self.title_depth:
            self.title_text += data
        if self.figcaption_depth:
            self.figcaption_text.append(data)

    def finish(self) -> list[str]:
        if not self.lang:
            self.error("missing html lang")
        if not self.title_text.strip():
            self.error("missing non-empty title")
        if not self.description:
            self.error("missing meta description")
        if not self.viewport:
            self.error("missing viewport meta")
        if self.canonical_count != 1 or self.canonical != self.expected_canonical:
            self.error(f"canonical must be exactly {self.expected_canonical}")
        if self.referrer_count != 1 or self.referrer != "no-referrer":
            self.error("referrer policy must be no-referrer")
        expected_robots = "noindex,follow" if self.path.name == "404.html" else "index,follow"
        if self.robots_count != 1 or self.robots != expected_robots:
            self.error(f"robots metadata must be {expected_robots}")
        if self.csp_count != 1:
            self.error("exactly one meta Content-Security-Policy is required")
        else:
            self.validate_csp()
        expected_images = 1 if self.path.name == "index.html" else 0
        if self.image_count != expected_images or self.approved_image_count != expected_images:
            self.error(f"expected {expected_images} approved remote image, found {self.approved_image_count} of {self.image_count} images")
        expected_sources = 1 if self.path.name == "index.html" else 0
        if self.source_anchor_count != expected_sources:
            self.error(f"expected {expected_sources} approved image source link, found {self.source_anchor_count}")
        caption = " ".join("".join(self.figcaption_text).split())
        expected_caption = REMOTE_IMAGE_CAPTION if self.path.name == "index.html" else ""
        if caption != expected_caption:
            self.error("image caption must exactly identify the source, Blizzard copyright, and Terran non-affiliation")
        if self.main_count != 1:
            self.error(f"expected one main element, found {self.main_count}")
        if self.h1_count != 1:
            self.error(f"expected one h1, found {self.h1_count}")
        if self.headings and self.headings[0] != 1:
            self.error("first heading must be h1")
        for previous, current in zip(self.headings, self.headings[1:]):
            if current > previous + 1:
                self.error(f"heading order jumps from h{previous} to h{current}")
        for fragment in self.fragments:
            if fragment not in self.ids:
                self.error(f"fragment link has no target: #{fragment}")
        for attribute, path_text, directory_url, project_rooted in self.local_urls:
            base = DOCS if project_rooted else self.path.parent
            candidate = (base / path_text).resolve()
            try:
                candidate.relative_to(DOCS.resolve())
            except ValueError:
                self.error(f"{attribute} escapes docs/: {path_text}")
                continue
            if directory_url:
                candidate /= "index.html"
            if not candidate.exists():
                self.error(f"{attribute} target does not exist: {path_text}")
        return self.errors

    def validate_csp(self) -> None:
        directives: dict[str, list[str]] = {}
        for section in self.csp.split(";"):
            parts = section.strip().split()
            if parts:
                name = parts[0].lower()
                if name in directives:
                    self.error(f"duplicate CSP directive: {name}")
                directives[name] = parts[1:]
        if "frame-ancestors" in directives:
            self.error("frame-ancestors is unsupported in a meta CSP")
        expected_directives = dict(CSP_BASE)
        if self.path.name == "index.html":
            expected_directives["img-src"] = ["'self'", REMOTE_IMAGE_HOST]
        if set(directives) != set(expected_directives):
            self.error("CSP must contain exactly the required directives")
        for directive, expected in expected_directives.items():
            if directives.get(directive) != expected:
                self.error(f"CSP requires {directive} {' '.join(expected)}")


def expected_canonical(path: Path) -> str:
    return ORIGIN if path.name == "index.html" else f"{ORIGIN}{path.name}"


def is_local_host(hostname: str) -> bool:
    normalized = hostname.rstrip(".").lower()
    if normalized == "localhost" or normalized.endswith(".localhost") or normalized.endswith(".local"):
        return True
    try:
        address = ipaddress.ip_address(normalized)
    except ValueError:
        return False
    return address.is_loopback or address.is_private or address.is_link_local or address.is_unspecified


def looks_like_image_url(value: str) -> bool:
    parsed = urlsplit(value)
    return parsed.hostname == urlsplit(REMOTE_IMAGE_URL).hostname or bool(
        re.search(r"\.(?:avif|gif|jpe?g|png|svg|webp)$", parsed.path, re.IGNORECASE)
    )


def validate_html(path: Path) -> list[str]:
    parser = DocumentParser(path, expected_canonical(path))
    try:
        source = path.read_text(encoding="utf-8")
        parser.feed(source)
        parser.close()
    except (OSError, UnicodeError) as exc:
        return [f"{path.relative_to(ROOT)}: cannot read HTML: {exc}"]
    errors = parser.finish()
    expected_occurrences = 2 if path.name == "index.html" else 0
    if source.count(REMOTE_IMAGE_URL) != expected_occurrences:
        errors.append(
            f"{path.relative_to(ROOT)}: approved image URL must appear exactly {expected_occurrences} times"
        )
    return errors


def validate_tree() -> list[str]:
    errors: list[str] = []
    regular_files: set[Path] = set()

    def inspect(directory: Path, relative_directory: Path = Path()) -> None:
        try:
            entries = list(os.scandir(directory))
        except OSError as exc:
            errors.append(f"{directory.relative_to(ROOT)}: cannot inspect directory: {exc}")
            return
        for entry in entries:
            relative = relative_directory / entry.name
            path = Path(entry.path)
            try:
                mode = entry.stat(follow_symlinks=False).st_mode
            except OSError as exc:
                errors.append(f"{path.relative_to(ROOT)}: cannot inspect path: {exc}")
                continue
            if stat.S_ISLNK(mode):
                errors.append(f"{path.relative_to(ROOT)}: symlinks are forbidden")
            elif stat.S_ISDIR(mode):
                if relative not in EXPECTED_DIRECTORIES:
                    errors.append(f"{path.relative_to(ROOT)}: unexpected directory")
                inspect(path, relative)
            elif stat.S_ISREG(mode):
                regular_files.add(relative)
                if relative not in EXPECTED_FILES:
                    errors.append(f"{path.relative_to(ROOT)}: unexpected file")
            else:
                errors.append(f"{path.relative_to(ROOT)}: non-regular files are forbidden")

    inspect(DOCS)
    for relative in sorted(EXPECTED_FILES - regular_files):
        errors.append(f"missing required file: {(DOCS / relative).relative_to(ROOT)}")
    return errors


def validate_sitemap(path: Path) -> list[str]:
    errors: list[str] = []
    try:
        root = ElementTree.parse(path).getroot()
    except (ElementTree.ParseError, OSError) as exc:
        return [f"{path.relative_to(ROOT)}: invalid XML: {exc}"]
    namespace = "{http://www.sitemaps.org/schemas/sitemap/0.9}"
    locations = [node.text.strip() for node in root.findall(f"{namespace}url/{namespace}loc") if node.text]
    if locations != [ORIGIN]:
        errors.append(f"{path.relative_to(ROOT)}: sitemap URLs must exactly match the index canonical")
    for location in locations:
        parsed = urlsplit(location)
        outside_origin = (
            parsed.scheme != ORIGIN_PARTS.scheme
            or parsed.netloc != ORIGIN_PARTS.netloc
            or not parsed.path.startswith(ORIGIN_PARTS.path)
        )
        if outside_origin:
            errors.append(f"{path.relative_to(ROOT)}: URL is outside production origin: {location}")
    return errors


def validate_robots(path: Path) -> list[str]:
    try:
        content = path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        return [f"{path.relative_to(ROOT)}: cannot read robots policy: {exc}"]
    if content != ROBOTS_CONTENT:
        return [f"{path.relative_to(ROOT)}: content must exactly allow public crawling and reference {SITEMAP_URL}"]
    return []


def validate_assets() -> list[str]:
    errors: list[str] = []
    css_path = DOCS / "assets" / "site.css"
    try:
        css = css_path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        errors.append(f"{css_path.relative_to(ROOT)}: cannot read CSS: {exc}")
    else:
        if re.search(r"@import\b", css, re.IGNORECASE):
            errors.append(f"{css_path.relative_to(ROOT)}: remote or nested CSS imports are forbidden")
        if re.search(r"url\(\s*['\"]?(?:https?:)?//", css, re.IGNORECASE):
            errors.append(f"{css_path.relative_to(ROOT)}: remote CSS resource is forbidden")
        if not re.search(r":focus-visible\b", css):
            errors.append(f"{css_path.relative_to(ROOT)}: missing focus-visible treatment")
        if not re.search(r"@media\s*\(prefers-reduced-motion:\s*reduce\)", css):
            errors.append(f"{css_path.relative_to(ROOT)}: missing reduced-motion media query")
    for svg_path in sorted((DOCS / "assets").glob("*.svg")):
        errors.extend(validate_svg(svg_path))
    return errors


def validate_public_urls() -> list[str]:
    errors: list[str] = []
    for path in sorted(candidate for candidate in DOCS.rglob("*") if candidate.is_file()):
        try:
            source = path.read_text(encoding="utf-8")
        except (OSError, UnicodeError) as exc:
            errors.append(f"{path.relative_to(ROOT)}: cannot scan URLs: {exc}")
            continue
        for value in re.findall(r"(?:https?|file)://[^\s<>\"']+", source, re.IGNORECASE):
            parsed = urlsplit(value.rstrip(".,);"))
            if parsed.scheme.lower() == "file" or (parsed.hostname and is_local_host(parsed.hostname)):
                errors.append(f"{path.relative_to(ROOT)}: local-only URL is forbidden: {value}")
    return errors


def local_name(name: str) -> str:
    return name.rsplit("}", 1)[-1].lower()


def namespace(name: str) -> str:
    return name[1:].split("}", 1)[0] if name.startswith("{") else ""


def validate_svg(path: Path) -> list[str]:
    errors: list[str] = []
    try:
        source = path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        return [f"{path.relative_to(ROOT)}: invalid SVG: {exc}"]
    if "<?" in source:
        return [f"{path.relative_to(ROOT)}: SVG processing instructions are forbidden"]
    try:
        root = ElementTree.fromstring(source)
    except ElementTree.ParseError as exc:
        return [f"{path.relative_to(ROOT)}: invalid SVG: {exc}"]
    if "<!doctype" in source.lower() or "<!entity" in source.lower():
        errors.append(f"{path.relative_to(ROOT)}: SVG declarations and entities are forbidden")
    if local_name(root.tag) != "svg":
        errors.append(f"{path.relative_to(ROOT)}: SVG root element is required")
    for element in root.iter():
        tag = local_name(element.tag)
        if namespace(element.tag) != SVG_NAMESPACE:
            errors.append(f"{path.relative_to(ROOT)}: SVG elements must use the SVG namespace")
        if tag in SVG_FORBIDDEN_TAGS or tag not in SVG_ALLOWED_TAGS:
            errors.append(f"{path.relative_to(ROOT)}: forbidden SVG element <{tag}>")
        for raw_name, value in element.attrib.items():
            attribute = local_name(raw_name)
            normalized = value.strip().lower()
            if attribute.startswith("on"):
                errors.append(f"{path.relative_to(ROOT)}: SVG event-handler attribute is forbidden: {attribute}")
            if attribute == "style":
                errors.append(f"{path.relative_to(ROOT)}: SVG style attributes are forbidden")
            if attribute in {"href", "src"}:
                errors.append(f"{path.relative_to(ROOT)}: SVG links and external references are forbidden")
            if re.search(r"(?:javascript:|data:|https?:|file:|//)", normalized):
                errors.append(f"{path.relative_to(ROOT)}: SVG attribute contains a remote or active URL")
            for match in re.findall(r"url\(([^)]+)\)", normalized):
                if not match.strip(" '\"").startswith("#"):
                    errors.append(f"{path.relative_to(ROOT)}: SVG url() must be a local fragment")
    return errors


def main(argv: list[str] | None = None) -> int:
    arguments = sys.argv[1:] if argv is None else argv
    if len(arguments) > 1:
        print("usage: validate-docs.py [docs-directory]", file=sys.stderr)
        return 2
    requested_docs = (ROOT / arguments[0]).resolve() if arguments else DOCS.resolve()
    if requested_docs != DOCS.resolve():
        print(f"documentation directory must be {DOCS.relative_to(ROOT)}", file=sys.stderr)
        return 2
    errors = validate_tree()
    if not errors:
        for html_path in sorted(DOCS.glob("*.html")):
            errors.extend(validate_html(html_path))
        errors.extend(validate_sitemap(DOCS / "sitemap.xml"))
        errors.extend(validate_robots(DOCS / "robots.txt"))
        errors.extend(validate_assets())
        errors.extend(validate_public_urls())
    if errors:
        print("Documentation validation failed:", file=sys.stderr)
        for error in errors:
            print(f"  - {error}", file=sys.stderr)
        return 1
    print(f"Documentation validation passed ({len(EXPECTED_FILES)} exact files, {len(list(DOCS.glob('*.html')))} HTML pages).")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
