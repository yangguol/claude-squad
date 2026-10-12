import styles from "./page.module.css";
import dynamic from "next/dynamic";

// Import ThemeToggle and CopyButton dynamically to prevent hydration issues
const ThemeToggle = dynamic(() => import("./components/ThemeToggle"), {
});

const CopyButton = dynamic(() => import("./components/CopyButton"), {
});

export default function Home() {
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <h1 className={styles.title}>claude squad</h1>
        <div className={styles.headerActions}>
          <a
            className={styles.headerButton}
            href="https://github.com/yangguol/claude-squad"
            target="_blank"
            rel="noopener noreferrer"
          >
            GitHub
          </a>
          <a
            href="https://github.com/yangguol/claude-squad#readme"
            target="_blank"
            rel="noopener noreferrer"
            className={styles.headerButton}
          >
            Docs
          </a>
          <ThemeToggle />
        </div>
      </header>
      <main className={styles.main}>
        
        
        <p className={styles.tagline}>
          Manage multiple AI agents like <span className={styles.highlight}>Claude Code</span>, <span className={styles.highlight}>Codex</span>, and <span className={styles.highlight}>Aider</span>. <br/><span className={styles.tenx}>10x</span> your productivity
        </p>

        <div className={styles.demoVideo}>
          <video 
            controls
            autoPlay
            muted
            loop
            playsInline
            className={styles.video}
            src="https://github.com/user-attachments/assets/aef18253-e58f-4525-9032-f5a3d66c975a"
          />
        </div>

        <div className={styles.installation}>
          <h2>Installation</h2>
          <h3>Build this fork from source</h3>
          <div className={styles.codeBlockWrapper}>
            <pre className={styles.codeBlock}>
              <code>git clone https://github.com/yangguol/claude-squad.git &amp;&amp; cd claude-squad &amp;&amp; go build -o cs . &amp;&amp; mkdir -p ~/.local/bin &amp;&amp; install -m 0755 cs ~/.local/bin/cs</code>
            </pre>
            <CopyButton textToCopy="git clone https://github.com/yangguol/claude-squad.git && cd claude-squad && go build -o cs . && mkdir -p ~/.local/bin && install -m 0755 cs ~/.local/bin/cs" />
          </div>
          <p className={styles.prerequisites}>
            Prerequisites: Git, tmux, Go 1.23+ (toolchain 1.25.8); gh is optional for GitHub handoff
          </p>
        </div>
        
        <div className={styles.features}>
          <h2>Why use Claude Squad?</h2>
          <ul>
            <li>Supervise multiple agents in one UI</li>
            <li>Isolate tasks in Git repositories or ordinary directories</li>
            <li>Apply directory-session changes with three-way merge and crash recovery</li>
            <li>Review work before it reaches your source directory</li>
            <li>Ordinary-directory mode supports Darwin and Linux</li>
          </ul>
        </div>
      </main>
      <footer className={styles.footer}>
        <p className={styles.copyright}>
          &copy; {new Date().getFullYear()} Claude Squad. Licensed under <a href="https://github.com/yangguol/claude-squad/blob/main/LICENSE.md" target="_blank" rel="noopener noreferrer">GNU AGPL v3.0</a>
        </p>
      </footer>
    </div>
  );
}
