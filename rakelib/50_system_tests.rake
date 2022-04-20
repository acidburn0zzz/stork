# System tests
# Run the system tests in docker-compose

#############
### Files ###
#############

system_tests_dir = "tests/system"
kea_many_subnets_dir = "tests/system/config/kea-many-subnets"
directory kea_many_subnets_dir
kea_many_subnets_config_file = File.join(kea_many_subnets_dir, "kea-dhcp4.conf")
file kea_many_subnets_config_file => [kea_many_subnets_dir] do
    sh "python3", "docker/tools/gen-kea-config.py", "7000", "-o",
       kea_many_subnets_config_file
end
CLEAN.append kea_many_subnets_dir

CLEAN.append "tests/system/config/kea/kea-leases4.csv"
CLEAN.append "tests/system/config/kea/kea-leases6.csv"

#########################
### System test tasks ###
#########################

desc 'Run system tests
    TEST - Name of the test to run - optional'
task :system_tests => [PYTEST, kea_many_subnets_config_file] do
    opts = []
    if !ENV["TEST"].nil?
        opts.append "-k", ENV["TEST"]
    end
    Dir.chdir(system_tests_dir) do
        sh PYTEST, "-s", *opts
    end
end

desc 'Build the containers used in the system tests'
task :build_system_tests_containers do
    Rake::Task["run_system_tests_compose"].invoke("build")
end

desc 'Run system tests docker-compose
    USE_BUILD_KIT - use BuildKit for faster build - default: true
'
task :run_system_tests_compose do |t, args|
    if ENV["USE_BUILD_KIT"] != "false"
        ENV["COMPOSE_DOCKER_CLI_BUILD"] = "1"
        ENV["DOCKER_BUILDKIT"] = "1"
    end

    ENV["PWD"] = Dir.pwd

    sh "docker-compose",
        "-f", File.expand_path(File.join(system_tests_dir, "docker-compose.yaml")),
        "--project-directory", File.expand_path("."),
        "--project-name", "stork_tests",
        *args
end

desc 'Recreate autogenerated configs'
task :regenerate_configs do
    items = [
        kea_many_subnets_config_file
    ]

    items.each do |f|
        FileUtils.rm_rf(f)
        Rake::FileTask[f].invoke()
    end
end

desc 'Install the external dependencies related to the system tests'
task :prepare_env_system_tests do
    find_and_prepare_deps(__FILE__)
end

desc 'Check the external dependencies related to the system tests'
task :check_env_system_tests do
    check_deps(__FILE__, "python3", "pip3", "docker", "docker-compose")
end