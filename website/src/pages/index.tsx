import type { ReactNode } from "react";
import clsx from "clsx";
import Link from "@docusaurus/Link";
import useDocusaurusContext from "@docusaurus/useDocusaurusContext";
import Layout from "@theme/Layout";
import Heading from "@theme/Heading";

import styles from "./index.module.css";

function HomepageHeader() {
  const { siteConfig } = useDocusaurusContext();
  return (
    <header className={clsx("hero hero--primary", styles.heroBanner)}>
      <div className="container">
        <Heading as="h1" className="hero__title">
          {siteConfig.title}
        </Heading>
        <p className="hero__subtitle">{siteConfig.tagline}</p>
        <div className={styles.buttons}>
          <Link
            className="button button--secondary button--lg"
            to="/docs/intro"
          >
            Get Started
          </Link>
        </div>
      </div>
    </header>
  );
}

function Reason({
  index,
  title,
  children,
}: {
  index: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <div className={clsx("col col--4")}>
      <div className="padding-horiz--md">
        <span className={styles.reasonIndex}>{index}</span>
        <Heading as="h3">{title}</Heading>
        <p>{children}</p>
      </div>
    </div>
  );
}

export default function Home(): ReactNode {
  const { siteConfig } = useDocusaurusContext();
  return (
    <Layout
      title={`${siteConfig.title} - Notion to Markdown Sync`}
      description="ntnsync keeps a Notion workspace mirrored as plain markdown in git, so your LLM can read it, your history never expires, and your knowledge is never locked in."
    >
      <HomepageHeader />
      <main>
        <section className={styles.thesis}>
          <div className="container">
            <p className={styles.thesisLead}>
              ntnsync mirrors a Notion workspace into plain markdown files in a
              git repository, continuously. Every page &mdash; and every
              database, and every row of every database &mdash; becomes one{" "}
              <code>.md</code> file whose path never changes, committed on every
              sync.
            </p>
          </div>
        </section>

        <section className={styles.features}>
          <div className="container">
            <Heading as="h2" className={styles.sectionTitle}>
              Why you would want this
            </Heading>
            <div className="row">
              <Reason index="01" title="Your LLM can actually read it">
                The Notion API makes an agent paginate a block tree, one page at
                a time. A markdown tree is grep-able and chunk-able, so the whole
                workspace fits into a coding agent&rsquo;s context the same way
                your source code does.
              </Reason>
              <Reason index="02" title="History with no expiry date">
                Notion&rsquo;s page history is capped by your plan. Git&rsquo;s
                is not capped at all. Every sync is a commit, so &ldquo;what did
                this spec say in March?&rdquo; is a <code>git log</code> away
                &mdash; forever.
              </Reason>
              <Reason index="03" title="No lock-in, no cliff edge">
                If the subscription lapses or the team moves tools, nothing has
                to be rescued. The knowledge is already on disk, in the one
                format every editor, search tool and model already reads.
              </Reason>
            </div>
          </div>
        </section>

        <section className={styles.contrast}>
          <div className="container">
            <div className="row">
              <div className="col col--6">
                <Heading as="h2" className={styles.sectionTitle}>
                  &ldquo;Notion already exports markdown&rdquo;
                </Heading>
                <p>
                  It does &mdash; once, by hand, into a fresh folder with fresh
                  filenames. ntnsync is different in three ways that matter:
                </p>
                <ul>
                  <li>
                    It runs <strong>continuously</strong>: pull the pages that
                    changed, sync, repeat &mdash; on a schedule or on a webhook.
                  </li>
                  <li>
                    File paths are <strong>stable</strong>. Renaming a page in
                    Notion does not move its file, so a diff shows what changed
                    in the content instead of a delete plus an add.
                  </li>
                  <li>
                    Every version lands in <strong>git</strong>. Because the
                    paths hold still, the diffs actually mean something.
                  </li>
                </ul>
              </div>
              <div className="col col--6">
                <Heading as="h2" className={styles.sectionTitle}>
                  One-way, and read-only
                </Heading>
                <p>
                  ntnsync reads Notion and writes files. It never writes back to
                  Notion &mdash; not a page, not a property, not a comment.
                  Editing the markdown changes nothing in your workspace, and the
                  next sync will overwrite it.
                </p>
                <p>
                  So this is a <em>mirror</em>, not a backup you can restore
                  into. What you get is readable, searchable, permanently
                  versioned markdown &mdash; not a workspace in a box.
                </p>
              </div>
            </div>
            <div className={styles.buttons}>
              <Link className="button button--primary button--lg" to="/docs/intro">
                See how it works
              </Link>
            </div>
          </div>
        </section>
      </main>
    </Layout>
  );
}
