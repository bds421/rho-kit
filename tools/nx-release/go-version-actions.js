"use strict";

const { join } = require("node:path");
const { execFileSync } = require("node:child_process");
const { VersionActions } = require("nx/release");

const MANIFEST_FILENAME = "go.mod";

/**
 * Custom Go version actions that extend NX Release VersionActions.
 *
 * Based on @naxodev/gonx's GoVersionActions but implements
 * readCurrentVersionOfDependency to support inter-module dependency
 * resolution from git tags in a Go monorepo.
 */
class GoVersionActions extends VersionActions {
  constructor() {
    super(...arguments);
    this.validManifestFilenames = [MANIFEST_FILENAME];
  }

  /**
   * Go modules do not store their own version in go.mod.
   * Returns 0.0.0 as fallback (used for first-release scenarios).
   * The git-tag strategy overrides this with the actual tag-based version.
   */
  async readCurrentVersionFromSourceManifest(tree) {
    return {
      currentVersion: "0.0.0",
      manifestPath: join(this.projectGraphNode.data.root, "go.mod"),
    };
  }

  /**
   * Retrieve the module version from the Go module proxy (proxy.golang.org).
   */
  async readCurrentVersionFromRegistry(tree, currentVersionResolverMetadata) {
    const manifestPath = join(
      this.projectGraphNode.data.root,
      MANIFEST_FILENAME
    );
    const content = tree.read(manifestPath, "utf-8");
    const moduleNameMatch = content.match(/module\s+([^\s]+)/);
    const moduleName = moduleNameMatch ? moduleNameMatch[1] : "";

    const result = await fetch(
      `https://proxy.golang.org/${encodeURIComponent(moduleName)}/@latest`
    );
    if (result && result.ok) {
      const response = await result.json();
      const latestVersion = response.Version;
      return {
        currentVersion: latestVersion,
        logText: `Retrieved version ${latestVersion} from proxy.golang.org for ${moduleName}`,
      };
    }

    throw new Error(
      `Unable to determine the current version of "${this.projectGraphNode.name}" from proxy.golang.org.`
    );
  }

  /**
   * Read the current version of a dependency from go.mod require directives.
   *
   * Parses the go.mod file of the current project to find the required version
   * of the dependency module. Falls back to reading from git tags if not found.
   */
  readCurrentVersionOfDependency(tree, projectGraph, dependencyProjectName) {
    const depNode = projectGraph.nodes[dependencyProjectName];
    if (!depNode) {
      return {
        currentVersion: "0.0.0",
        dependencyType: "static",
      };
    }

    // Read the dependency's go.mod to get its module name
    const depModPath = join(depNode.data.root, "go.mod");
    const depModContent = tree.read(depModPath, "utf-8");
    const depModuleMatch = depModContent.match(/module\s+([^\s]+)/);
    const depModuleName = depModuleMatch ? depModuleMatch[1] : "";

    // Read this project's go.mod to find the required version of the dependency
    const projectModPath = join(
      this.projectGraphNode.data.root,
      "go.mod"
    );
    const projectModContent = tree.read(projectModPath, "utf-8");

    // Match require directives: require github.com/foo/bar v1.2.3
    // Also handle require blocks: require ( \n github.com/foo/bar v1.2.3 \n )
    const escapedModName = depModuleName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const requirePattern = new RegExp(
      `${escapedModName}\\s+v([\\d]+\\.[\\d]+\\.[\\d]+(?:-[\\w.]+)?)`,
      "m"
    );
    const match = projectModContent.match(requirePattern);

    if (match) {
      return {
        currentVersion: match[1],
        dependencyType: "static",
      };
    }

    // Fallback: try to read from git tags using execFileSync (safe, no shell)
    try {
      const tagPrefix = `${dependencyProjectName}/v`;
      const output = execFileSync(
        "git",
        ["tag", "--list", `${tagPrefix}*`, "--sort=-version:refname"],
        { encoding: "utf-8" }
      );
      const tags = output.trim().split("\n").filter(Boolean);

      if (tags.length > 0) {
        const version = tags[0].replace(tagPrefix, "");
        return {
          currentVersion: version,
          dependencyType: "static",
        };
      }
    } catch {
      // git command failed, fall through
    }

    return {
      currentVersion: "0.0.0",
      dependencyType: "static",
    };
  }

  /**
   * Go modules do not store their version in go.mod, so this is a no-op.
   */
  async updateProjectVersion(tree, newVersion) {
    return [];
  }

  /**
   * Update the dependency version in go.mod require directives.
   *
   * When a sibling module gets a new version, this updates the require
   * directive in this project's go.mod to point to the new version.
   */
  async updateProjectDependencies(tree, projectGraph, dependenciesToUpdate) {
    // dependenciesToUpdate is an object: { [projectName]: "newVersion" }
    const entries = Object.entries(dependenciesToUpdate || {});
    if (entries.length === 0) {
      return [];
    }

    const projectModPath = join(
      this.projectGraphNode.data.root,
      "go.mod"
    );
    let content = tree.read(projectModPath, "utf-8");
    const logMessages = [];

    for (const [depName, newVersion] of entries) {
      const depNode = projectGraph.nodes[depName];
      if (!depNode) {
        continue;
      }

      // Read the dependency's module name from its go.mod
      const depModPath = join(depNode.data.root, "go.mod");
      const depModContent = tree.read(depModPath, "utf-8");
      const depModuleMatch = depModContent.match(/module\s+([^\s]+)/);
      const depModuleName = depModuleMatch ? depModuleMatch[1] : "";

      if (!depModuleName) {
        continue;
      }

      // Strip any prefix characters from the version (e.g., ^, ~, =)
      const cleanVersion = newVersion.replace(/^[~^=v]/, "");

      // Replace the version in require directive
      const escapedModName = depModuleName.replace(
        /[.*+?^${}()|[\]\\]/g,
        "\\$&"
      );
      const requirePattern = new RegExp(
        `(${escapedModName}\\s+v)[\\d]+\\.[\\d]+\\.[\\d]+(?:-[\\w.]+)?`,
        "gm"
      );

      const newContent = content.replace(
        requirePattern,
        `$1${cleanVersion}`
      );

      if (newContent !== content) {
        content = newContent;
        logMessages.push(
          `Updated ${depModuleName} to v${cleanVersion} in go.mod`
        );
      }
    }

    if (logMessages.length > 0) {
      tree.write(projectModPath, content);

      // Run `go mod tidy` to recompute go.sum after version changes.
      // The tree.write above flushes go.mod to disk, so `go mod tidy`
      // sees the updated versions and regenerates checksums.
      const projectRoot = this.projectGraphNode.data.root;
      try {
        execFileSync("go", ["mod", "tidy"], {
          cwd: projectRoot,
          encoding: "utf-8",
          stdio: "pipe",
          env: {
            ...process.env,
            // Use the workspace so sibling modules resolve locally
            // (their new versions aren't published yet when this runs).
            GOWORK: join(process.cwd(), "go.work"),
          },
        });
        // Read the updated go.sum back into the tree so it's included
        // in the release commit.
        const goSumPath = join(projectRoot, "go.sum");
        try {
          const goSumContent = require("node:fs").readFileSync(
            goSumPath,
            "utf-8"
          );
          tree.write(goSumPath, goSumContent);
        } catch {
          // go.sum may not exist for modules with no external deps
        }
        logMessages.push("Ran go mod tidy to update go.sum");
      } catch (e) {
        // Log but don't fail — go.sum staleness is non-fatal with GONOSUMCHECK.
        logMessages.push(
          `Warning: go mod tidy failed (${e.message}), go.sum may be stale`
        );
      }
    }

    return logMessages;
  }
}

module.exports = GoVersionActions;
module.exports.default = GoVersionActions;
