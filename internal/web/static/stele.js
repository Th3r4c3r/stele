// Stele client glue: theme toggle + keyboard shortcuts.
// Pure ES2017, no framework. Runs once after DOMContentLoaded.

(function () {
	"use strict";

	// --- Theme toggle ---------------------------------------------
	// Preference is stored in localStorage under "stele-theme":
	//   "dark" | "light" | (absent) => follow OS prefers-color-scheme.
	// The inline boot script in <head> sets data-theme before paint
	// so there is no FOUC. This file handles the toggle button.

	function currentTheme() {
		var attr = document.documentElement.getAttribute("data-theme");
		if (attr === "dark" || attr === "light") return attr;
		// Honour OS preference when nothing is pinned.
		return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches
			? "dark"
			: "light";
	}

	function setTheme(theme) {
		document.documentElement.setAttribute("data-theme", theme);
		try { localStorage.setItem("stele-theme", theme); } catch (e) {}
		updateToggleLabel();
	}

	function updateToggleLabel() {
		var btn = document.querySelector("[data-theme-toggle]");
		if (!btn) return;
		var t = currentTheme();
		// Inline glyph: half-moon for both, but we tilt by colour:
		// light theme button hints "go dark" (filled ◑), dark theme
		// hints "go light" (outlined ◐). Title updates similarly.
		btn.textContent = t === "dark" ? "◐" : "◑";
		btn.title = t === "dark" ? "Switch to light theme" : "Switch to dark theme";
		btn.setAttribute("aria-label", btn.title);
	}

	function initThemeToggle() {
		var btn = document.querySelector("[data-theme-toggle]");
		if (!btn) return;
		updateToggleLabel();
		btn.addEventListener("click", function () {
			setTheme(currentTheme() === "dark" ? "light" : "dark");
		});
	}

	// --- Keyboard shortcuts ---------------------------------------
	// /     focus the global search input
	// n     navigate to /cases/new
	// j/k   move keyboard "selection" down/up in the cases table
	// Enter open the selected row
	// Esc   blur / close open <details>
	//
	// Shortcuts are skipped when the user is typing in an input,
	// textarea, or contenteditable element.

	function isTyping(e) {
		var t = e.target;
		if (!t) return false;
		var tag = (t.tagName || "").toLowerCase();
		if (tag === "input" || tag === "textarea" || tag === "select") return true;
		if (t.isContentEditable) return true;
		return false;
	}

	function focusSearch() {
		var input = document.querySelector(".search-bar input[type=search]");
		if (input) { input.focus(); input.select && input.select(); }
	}

	function highlightedRow() {
		return document.querySelector("tbody tr.kbd-selected");
	}

	function caseRows() {
		// Pick the rows from the currently-visible cases table. The
		// /cases page has multiple tabs but only one is shown at a
		// time via :target / class; we just grab all visible rows.
		var rows = document.querySelectorAll("table.claims tbody tr");
		return Array.prototype.filter.call(rows, function (tr) {
			return tr.offsetParent !== null; // visible
		});
	}

	function moveSelection(delta) {
		var rows = caseRows();
		if (!rows.length) return;
		var current = highlightedRow();
		var idx = current ? rows.indexOf(current) : -1;
		idx = Math.max(0, Math.min(rows.length - 1, idx + delta));
		rows.forEach(function (r) { r.classList.remove("kbd-selected"); });
		rows[idx].classList.add("kbd-selected");
		// Scroll the highlighted row into view if it's offscreen.
		var rect = rows[idx].getBoundingClientRect();
		if (rect.top < 60 || rect.bottom > window.innerHeight - 20) {
			rows[idx].scrollIntoView({ block: "center", behavior: "smooth" });
		}
	}

	function openSelected() {
		var row = highlightedRow();
		if (!row) return;
		var link = row.querySelector("a[href]");
		if (link) link.click();
	}

	function closeOpenDetails() {
		document.querySelectorAll("details[open]").forEach(function (d) {
			d.removeAttribute("open");
		});
	}

	function onKeyDown(e) {
		if (e.altKey || e.ctrlKey || e.metaKey) return;
		if (isTyping(e)) {
			if (e.key === "Escape") e.target.blur();
			return;
		}
		switch (e.key) {
			case "/":
				e.preventDefault();
				focusSearch();
				return;
			case "n":
				e.preventDefault();
				window.location.href = "/cases/new";
				return;
			case "j":
				e.preventDefault();
				moveSelection(1);
				return;
			case "k":
				e.preventDefault();
				moveSelection(-1);
				return;
			case "Enter":
				if (highlightedRow()) { e.preventDefault(); openSelected(); }
				return;
			case "Escape":
				closeOpenDetails();
				return;
			case "?":
				e.preventDefault();
				alert(
					"Stele keyboard shortcuts\n\n" +
					"  /         focus search\n" +
					"  n         open new case form\n" +
					"  j / k     move selection down / up in case lists\n" +
					"  Enter     open the selected case\n" +
					"  Esc       close open details / blur input\n" +
					"  ?         show this help"
				);
				return;
		}
	}

	// --- Boot -----------------------------------------------------
	if (document.readyState === "loading") {
		document.addEventListener("DOMContentLoaded", function () {
			initThemeToggle();
			document.addEventListener("keydown", onKeyDown);
		});
	} else {
		initThemeToggle();
		document.addEventListener("keydown", onKeyDown);
	}
})();
