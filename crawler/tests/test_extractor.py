from collision_crawler.extractor import extract_article

from .conftest import fixture_text


def test_extracts_article_and_metadata():
    out = extract_article(fixture_text("pedestrian_fatality.html"), "https://news.example.com/x")
    assert "International Boulevard" in out.text
    assert "pronounced dead" in out.text
    assert "Home" not in out.text.split("\n")[0]  # nav stripped
    assert out.title.startswith("Pedestrian killed in Oakland")
    assert out.publisher == "Example Gazette"
    assert out.author == "Jane Reporter"
    assert out.language == "en"
    assert out.canonical_url == "https://news.example.com/2026/10/pedestrian-killed-oakland"
    assert out.published_at is not None and out.published_at.year == 2026 and out.published_at.month == 10


def test_js_shell_yields_no_text():
    assert extract_article(fixture_text("thin_js_page.html"), "https://x.example.com/").text == ""


def test_implausible_author_dropped():
    from collision_crawler.extractor import _clean_author
    assert _clean_author('Name "Great Streets; Bus" Scribd com Retrieved') == ""
    assert _clean_author("Jane Reporter") == "Jane Reporter"
    assert _clean_author(None) == ""
