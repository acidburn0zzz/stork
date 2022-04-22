# System tests
# Run the system tests in docker-compose

#############
### Files ###
#############

autogenerated = []

system_tests_dir = "tests/system"
kea_many_subnets_dir = "tests/system/config/kea-many-subnets"
directory kea_many_subnets_dir
kea_many_subnets_config_file = File.join(kea_many_subnets_dir, "kea-dhcp4.conf")
file kea_many_subnets_config_file => [kea_many_subnets_dir] do
    sh "python3", "docker/tools/gen-kea-config.py", "7000", "-o",
       kea_many_subnets_config_file
end
autogenerated.append kea_many_subnets_dir

# These files are generated by the system tests.
CLEAN.append "tests/system/config/kea/kea-leases4.csv"
CLEAN.append "tests/system/config/kea/kea-leases6.csv"

# TLS credentials
tls_dir = "tests/system/config/certs"
cert_file = File.join(tls_dir, "cert.pem")
key_file = File.join(tls_dir, "key.pem")
ca_dir = File.join(tls_dir, "CA")
directory ca_dir

file cert_file => [ca_dir] do
    sh "openssl", "req", "-x509", "-newkey", "rsa:4096",
        "-sha256", "-days", "3650", "-nodes",
        "-keyout", key_file, "-out", cert_file,
        "-subj", "/CN=kea.isc.org", "-addext",
        "subjectAltName=DNS:kea.isc.org,DNS:www.kea.isc.org,IP:127.0.0.1"
end
file key_file => [cert_file]
autogenerated.append cert_file, key_file, ca_dir

CLEAN.append *autogenerated

#########################
### System test tasks ###
#########################

desc 'Run system tests
    TEST - Name of the test to run - optional'
task :system_tests => [PYTEST, kea_many_subnets_config_file, key_file, cert_file] do
    opts = []
    if !ENV["TEST"].nil?
        opts.append "-k", ENV["TEST"]
    end
    Dir.chdir(system_tests_dir) do
        sh PYTEST, "-s", *opts
    end
end

namespace :system_tests do

desc 'Build the containers used in the system tests'
task :build do
    Rake::Task["system_tests:sh"].invoke("build")
end

desc 'Run shell in the docker-compose container
    SERVICE - name of the docker-compose service - required'
task :shell do
    Rake::Task["system_tests:sh"].invoke(
        "exec", ENV["SERVICE"], "/bin/sh")
end

desc 'Display docker-compose logs
    SERVICE - name of the docker-compose service - optional'
task :logs do
    service_name = ENV["SERVICE"]
    if service_name.nil?
        Rake::Task["system_tests:sh"].invoke("logs")
    else 
        Rake::Task["system_tests:sh"].invoke("logs", service_name)
    end
end

desc 'Run system tests docker-compose
    USE_BUILD_KIT - use BuildKit for faster build - default: true
'
task :sh do |t, args|
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

desc 'Create autogenerated configs'
task :gen do
    autogenerated.each do |f|
        Rake::FileTask[f].invoke()
    end
end

desc 'Recreate autogenerated configs'
task :regen do
    autogenerated.each do |f|
        FileUtils.rm_rf(f)
    end

    Rake::Task["system_tests:gen"].invoke()
end

end

desc 'Install the external dependencies related to the system tests'
task :prepare_env_system_tests do
    find_and_prepare_deps(__FILE__)
end

desc 'Check the external dependencies related to the system tests'
task :check_env_system_tests do
    check_deps(__FILE__, "python3", "pip3", "docker", "docker-compose", "openssl")
end
