// --- State ---
let currentSession = null;
let treeData = null;
let activePath = null;

// --- Init ---
document.addEventListener('DOMContentLoaded', () => {
  loadSessions();
  setInterval(refreshTree, 1000);
});

// --- Sessions ---
async function loadSessions() {
  try {
    const resp = await fetch('/api/sessions');
    const sessions = await resp.json();
    const sel = document.getElementById('session-selector');
    sel.innerHTML = '';

    if (sessions.length === 0) {
      sel.innerHTML = '<span style="color:var(--text-muted);font-size:13px;">No sessions yet — send a prompt to start.</span>';
      return;
    }

    const select = document.createElement('select');
    for (const s of sessions) {
      const opt = document.createElement('option');
      opt.value = s.id;
      opt.textContent = s.name;
      if (s.id === currentSession) opt.selected = true;
      select.appendChild(opt);
    }

    // Default to newest session.
    if (!currentSession && sessions.length > 0) {
      currentSession = sessions[sessions.length - 1].id;
      select.value = currentSession;
    }

    select.addEventListener('change', () => {
      currentSession = select.value;
      loadTree(true);
    });

    sel.appendChild(select);
    loadTree(true);
  } catch (e) {
    console.error('Failed to load sessions:', e);
  }
}

// --- Tree ---
async function loadTree(reset) {
  if (!currentSession) return;

  try {
    const resp = await fetch(`/api/sessions/${currentSession}/tree`);
    const newTree = await resp.json();

    if (reset || !treeData) {
      treeData = newTree;
      renderTreeFull();
    } else {
      // Merge new nodes into existing tree.
      mergeTree(treeData, newTree);
      renderTreeDiff(treeData, newTree);
      treeData = newTree;
    }
  } catch (e) {
    console.error('Failed to load tree:', e);
  }
}

async function refreshTree() {
  if (!currentSession) {
    loadSessions();
    return;
  }
  loadTree(false);
}

function renderTreeFull() {
  const ul = document.getElementById('tree');
  ul.innerHTML = '';
  if (treeData) {
    renderNode(ul, treeData);
  }
}

function renderNode(parentEl, node) {
  const li = document.createElement('li');
  const span = document.createElement('span');
  span.className = `tree-node ${node.type}`;
  span.textContent = node.name;
  span.dataset.path = node.path || '';
  span.dataset.type = node.type;

  if (node.path) {
    span.addEventListener('click', (e) => {
      e.stopPropagation();
      loadContent(node.path, span);
    });
  }

  li.appendChild(span);

  if (node.children && node.children.length > 0) {
    const childUl = document.createElement('ul');
    childUl.style.listStyle = 'none';
    for (const child of node.children) {
      renderNode(childUl, child);
    }
    li.appendChild(childUl);
  }

  parentEl.appendChild(li);
}

function mergeTree(oldNode, newNode) {
  // Recursively mark new children vs existing.
  if (!newNode) return;
  newNode._isNew = !oldNode;
}

function renderTreeDiff(oldTree, newTree) {
  // Walk new tree and add only truly new nodes (by path) to the DOM.
  const existingPaths = new Set();
  collectPaths(oldTree, existingPaths);

  const newNodes = [];
  collectNewNodes(newTree, existingPaths, newNodes);

  if (newNodes.length === 0) return;

  const ul = document.getElementById('tree');
  for (const node of newNodes) {
    // Find parent in DOM by path prefix and append.
    const parentPath = node._parentPath;
    if (parentPath) {
      const parentEl = findNodeByPath(ul, parentPath);
      if (parentEl) {
        const childUl = parentEl.querySelector('ul') || (() => {
          const u = document.createElement('ul');
          u.style.listStyle = 'none';
          parentEl.appendChild(u);
          return u;
        })();
        renderNodeWithMark(childUl, node, true);
      }
    }
  }
}

function collectPaths(node, set) {
  if (!node) return;
  if (node.path) set.add(node.path);
  if (node.children) {
    for (const c of node.children) collectPaths(c, set);
  }
}

function collectNewNodes(node, existingPaths, out, parentPath) {
  if (!node) return;
  node._parentPath = parentPath || '';
  if (node.path && !existingPaths.has(node.path)) {
    out.push(node);
  }
  if (node.children) {
    for (const c of node.children) {
      collectNewNodes(c, existingPaths, out, node._parentPath || '');
    }
  }
}

function findNodeByPath(parentEl, path) {
  // Walk DOM tree to find span with matching data-path.
  const spans = parentEl.querySelectorAll('span.tree-node');
  for (const s of spans) {
    if (s.dataset.path === path) return s.parentElement;
  }
  return null;
}

function renderNodeWithMark(parentEl, node, isNew) {
  const li = document.createElement('li');
  const span = document.createElement('span');
  span.className = `tree-node ${node.type}` + (isNew ? ' new' : '');
  span.textContent = node.name;
  span.dataset.path = node.path || '';
  span.dataset.type = node.type;

  if (node.path) {
    span.addEventListener('click', (e) => {
      e.stopPropagation();
      loadContent(node.path, span);
    });
  }

  li.appendChild(span);
  parentEl.appendChild(li);
}

// --- Content viewing ---
async function loadContent(logPath, spanEl) {
  // Highlight selected.
  document.querySelectorAll('.tree-node.selected').forEach(s => s.classList.remove('selected'));
  if (spanEl) spanEl.classList.add('selected');

  try {
    const resp = await fetch(`/api/log/${logPath}`);
    if (!resp.ok) throw new Error(resp.statusText);
    let text = await resp.text();

    // For JSON files, pretty-print with user-role highlighting.
    if (logPath.endsWith('.json')) {
      try {
        const obj = JSON.parse(text);
        text = JSON.stringify(obj, null, 2);
      } catch (_) { /* raw text */ }
      text = highlightUserRole(text);
    }

    // For JSONL files, pretty-print each line as JSON.
    if (logPath.endsWith('.jsonl')) {
      const lines = text.trim().split('\n');
      const prettyLines = lines.map(line => {
        try {
          return JSON.stringify(JSON.parse(line), null, 2);
        } catch (_) { return line; }
      });
      text = highlightUserRole(prettyLines.join('\n'));
    }

    document.getElementById('content-placeholder').style.display = 'none';
    const viewer = document.getElementById('content-viewer');
    viewer.style.display = 'block';
    viewer.innerHTML = text;
  } catch (e) {
    document.getElementById('content-placeholder').textContent = `Error: ${e.message}`;
  }
}

function highlightUserRole(jsonText) {
  // Split by lines; wrap lines containing "role": "user" in a highlight span.
  const lines = jsonText.split('\n');
  const result = [];
  for (const line of lines) {
    if (line.includes('"role"') && line.includes('"user"')) {
      result.push(`<span class="user-highlight">${escapeHtml(line)}</span>`);
    } else {
      result.push(escapeHtml(line));
    }
  }
  return result.join('\n');
}

function escapeHtml(str) {
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
