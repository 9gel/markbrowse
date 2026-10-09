{
  description = "markbrowse: read-only markdown folder viewer";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" ];
      forAll = f: nixpkgs.lib.genAttrs systems
        (s: f nixpkgs.legacyPackages.${s});
    in
    {
      packages = forAll (pkgs: {
        default = pkgs.buildGo126Module {
          pname = "markbrowse";
          version = self.shortRev or self.dirtyShortRev or "dev";
          src = self;
          vendorHash = "sha256-grY1/s3YLmoZ350lPp7McprmBvRKg++0uL5oHTKbNn8=";
          subPackages = [ "." ];
          ldflags = [ "-s" "-w" ];
          meta.mainProgram = "markbrowse";
        };
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go_1_26 ]; };
      });
    };
}
